package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"myGoRPC/codec"
	"net"
	"reflect"
	"sync"
)

const RpcNumber = 0x0312ff

/*
Option
定义消息的编解码方式
*/
type Option struct {
	RpcNumber int // 标志， myGoRPC 请求
	CodecType codec.Type
}

var DefaultOption = &Option{
	RpcNumber: RpcNumber,
	CodecType: codec.GobType,
}

/*
Server
定义了 RPC server
*/
type Server struct{}

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
	server.serveCodec(f(conn))
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
func (server *Server) serveCodec(cc codec.Codec) {
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
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	cc.Close()
}

type request struct {
	header *codec.Header
	argV   reflect.Value
	replyV reflect.Value
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
	// todo: 请求参数尚未确定，假定为string
	req.argV = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(req.argV.Interface()); err != nil {
		log.Println("rpc server: read argV err: ", err)
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
 */
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// todo: 应调用相应rpc方法，获取replyV，暂时只print参数
	defer wg.Done()
	log.Println("handleRequest header: ", req.header, req.argV.Elem())
	req.replyV = reflect.ValueOf(fmt.Sprintf("myGoRPC response %d", req.header.Seq))
	server.sendResponse(cc, req.header, req.replyV.Interface(), sending)
}

// ------------------ 构建默认 server ----------------

//var DefaultServer = NewServer()
//
//func Accept(listen net.Listener) { DefaultServer.Accept(listen) }
