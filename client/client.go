package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"myGoRPC/codec"
	"myGoRPC/server"
	"net"
	"sync"
	"time"
)

/*
Call

封装 rpc call 的信息，Service.Method 形式调用
*/
type Call struct {
	Seq     uint64
	Service string
	Method  string
	Args    interface{}
	Reply   interface{}
	Error   error
	Done    chan *Call
}

func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc       codec.Codec      // 消息的编解码器，序列化请求，以及反序列化响应
	option   *server.Option   // 编解码方式
	sending  sync.Mutex       // 保证请求的有序发送，防止出现多个请求报文混淆
	header   codec.Header     // 每个请求的消息头
	mu       sync.Mutex       // 保护以下
	seq      uint64           // 每个请求拥有唯一编号
	pending  map[uint64]*Call // 存储未处理完的请求，键是编号
	closing  bool             // 用户主动关闭的；值置为 true，则表示 Client 处于不可用的状态
	shutdown bool             // 一般有错误发生；值置为 true，则表示 Client 处于不可用的状态
}

// 确保实现
var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection has been shut down")

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

/*
IsAvailable
查询client是否关闭（主动关闭、错误关闭）
*/
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

/*
registerCall
参数 call 添加到 client.pending 中，并更新 client.seq
*/
func (client *Client) registerCall(call *Call) (seq uint64, err error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

/*
removeCall
根据 seq，从 client.pending 中移除对应的 call，并返回
*/
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

/*
terminateCalls
服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call
*/
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

/*
NewClient

创建 Client 实例；  完成协议交换；  创建子协程调用 receive 接受响应
*/
func NewClient(conn net.Conn, opt *server.Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s ", opt.CodecType)
		log.Println("rpc client: codec err: ", err)
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *server.Option) *Client {
	client := &Client{
		seq:     1, // starts with 1, 0 invalid call
		cc:      cc,
		option:  opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

/*
parseOptions

...*Option 将 Option 实现为可选参数
*/
func parseOptions(opts ...*server.Option) (*server.Option, error) {
	// 校验
	// opt is nil || pass nil as args
	if len(opts) == 0 || opts[0] == nil {
		return server.DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("options are more than 1")
	}

	opt := opts[0]
	opt.RpcNumber = server.DefaultOption.RpcNumber
	if opt.CodecType == "" {
		opt.CodecType = server.DefaultOption.CodecType
	}
	return opt, nil
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *server.Option) (client *Client, err error)

/*
dialTimeout

超时处理
*/
func dialTimeout(f newClientFunc, network, address string, opts ...*server.Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	// close conn if client is nil (sth wrong in func NewClient)
	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()

	ch := make(chan clientResult)
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	select {
	case <-time.After(opt.ConnectTimeout):
		// todo: 监测func未关闭，占用系统资源
		return nil, fmt.Errorf("rpc client: connection timeout")
	case result := <-ch:
		return result.client, result.err
	}
}

/*
Dial
调用 net.Dial, connects to the address on the named network.
添加外壳 dialTimeout
*/
func Dial(network, address string, opts ...*server.Option) (client *Client, err error) {

	return dialTimeout(NewClient, network, address, opts...)
}

/*
-------------- receive call -----------------

接收到的响应有三种情况：

- call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
- call 存在，但服务端处理出错，即 h.Error 不为空。
- call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值。
*/
func (client *Client) receive() {
	var err error
	for err == nil {
		var header codec.Header
		if err = client.cc.ReadHeader(&header); err != nil {
			break
		}
		call := client.removeCall(header.Seq)
		switch {
		case call == nil:
			// 有错误出现，call 已经被清除
			// cc.ReadBody 调用 gob.Decode，读入 nil，数据会被丢弃
			err = client.cc.ReadBody(nil)
		case header.Error != "":
			// 服务端处理出错
			call.Error = fmt.Errorf(header.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			// 正常处理
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}

// -------------- send call -----------------
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	// register
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// prepare header
	client.header.Service = call.Service
	client.header.Method = call.Method
	client.header.Seq = seq
	client.header.Error = ""

	// encode and send the request
	if err = client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		// 当 call 为 nil，意味着写入错误 / 客户端收到回复并处理过
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// ----------------- Invoke func --------------

func (client *Client) Go(service, method string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		Service: service,
		Method:  method,
		Args:    args,
		Reply:   reply,
		Done:    done,
	}
	client.send(call)
	return call
}

/*
Call

使用context包，超时处理
*/
func (client *Client) Call(ctx context.Context, service, method string, args, reply interface{}) error {
	call := client.Go(service, method, args, reply, make(chan *Call, 1))

	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
			return call.Error
	}
	return call.Error
}
