package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"myGoRPC/codec"
	"myGoRPC/service"
	"net"
	"reflect"
	"sync"
	"time"
)

const RpcNumber = 0x0312ff

/*
Option
定义消息的编解码方式
超时 0 即为无限制
*/
type Option struct {
	RpcNumber      int // 标志， myGoRPC 请求
	CodecType      codec.Type
	ConnectTimeout time.Duration
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	RpcNumber:      RpcNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

/*
Server
定义了 RPC server
*/
type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

/*
Accept
实现了 Accept 方式，net.Listener 作为参数，
for 循环等待 socket 连接建立，
并开启子协程处理，处理过程交给了 ServerConn 方法
*/
func (server *Server) Accept(listen net.Listener) {
	for {
		conn, err := listen.Accept()
		if err != nil {
			log.Println("rpc server: accept error: ", err)
			return
		}
		go server.ServeConn(conn)
	}
}

/*
ServeConn
首先使用 json.NewDecoder 反序列化得到 Option 实例，
检查 RpcNumber 和 CodeType 的值是否正确。
然后根据 CodeType 得到对应的消息编解码器，
接下来的处理交给 serverCodec
*/
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close()
	}()

	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options decode error: ", err)
		return
	}
	if opt.RpcNumber != RpcNumber {
		log.Printf("rpc server: invalid rpc number %x", opt.RpcNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// 定义非法请求的回应
var invalidRequest = struct{}{}

/*
serveCodec
读取、处理、回复请求 read/handle/send Response

在一次连接中，允许接收多个请求，即多个 request header 和 request body，
因此这里使用了 for 无限制地等待请求的到来，直到发生错误（例如连接被关闭，接收到的报文有问题等）

handleRequest 使用了协程并发执行请求

处理请求是并发的，但是回复请求的报文必须是逐个发送的，并发容易导致多个回复报文交织在一起，客户端无法解析。在这里使用锁(sending)保证

只有在 header 解析失败时，才终止循环
*/
func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		// 读取请求
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break
			}
			req.header.Error = err.Error()
			server.sendResponse(cc, req.header, invalidRequest, sending)
			continue
		}
		// 处理请求
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	cc.Close()
}

type request struct {
	header *codec.Header
	argV   reflect.Value
	replyV reflect.Value
	mtype  *service.MethodType
	svc    *service.Service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error: ", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{header: h}
	//  请求参数尚未确定，假定为string

	req.svc, req.mtype, err = server.findServiceMethod(h.Service, h.Method)
	if err != nil {
		return req, err
	}

	req.argV = req.mtype.NewArgv()
	req.replyV = req.mtype.NewReplyv()

	// 确保 argvi 是 指针
	argvi := req.argV.Interface()
	if req.argV.Type().Kind() != reflect.Ptr {
		argvi = req.argV.Addr().Interface()
	}

	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argV err: ", err)
		return req, err
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, header *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(header, body); err != nil {
		log.Println("rpc server: write response error: ", err)
	}
}

/*
handleRequest
调用相应 rpc 方法，写入 req.replyV
而后调用 sendResponse

加入超时处理
*/
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	// 应调用相应rpc方法，获取replyV，暂时只print参数
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})

	// 加上 context 告知子协程退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func(ctx context.Context) {
		err := req.svc.Call(req.mtype, req.argV, req.replyV)
		called <- struct{}{}

		if err != nil {
			req.header.Error = err.Error()
			server.sendResponse(cc, req.header, invalidRequest, sending)
			sent <- struct{}{}
			return
		}

		server.sendResponse(cc, req.header, req.replyV.Interface(), sending)
		sent <- struct{}{}
		select {
		case <-ctx.Done():
			return
		}
	}(ctx)

	if timeout == 0 {
		<-called
		<-sent
		return
	}

	select {
	case <-time.After(timeout):
		// 如果在timeout后call才调用结束，但已经超时，直接返回，将不会接受called，存在goroutines泄露
		req.header.Error = fmt.Sprintf("rpc server: request handle timeout")
		server.sendResponse(cc, req.header, invalidRequest, sending)
	case <-called:
		<-sent
	}
}

// ------------------ 构建默认 server ----------------

//var DefaultServer = NewServer()
//
//func Accept(listen net.Listener) { DefaultServer.Accept(listen) }

// ------------------ 服务注册、服务发现 ---------------

func (server *Server) Register(rcvr interface{}) error {
	s := service.NewService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.Name, s); dup {
		return errors.New("rpc: service already defined: " + s.Name)
	}
	return nil
}

func (server *Server) findServiceMethod(serviceName, methodName string) (svc *service.Service, mtype *service.MethodType, err error) {
	if serviceName == "" || methodName == "" {
		err = errors.New("rpc server: serviceName/methodName request ill-formed: " + serviceName + "." + methodName)
		return
	}

	svci, ok := server.serviceMap.Load(serviceName)

	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}

	svc = svci.(*service.Service)
	mtype = svc.Method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method" + methodName)
	}
	return
}
