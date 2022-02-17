package xclient

import (
	"context"
	"io"
	"myGoRPC"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *myGoRPC.Option
	mu      sync.Mutex
	clients map[string]*myGoRPC.Client
}

var _ io.Closer = (*XClient)(nil)

func (xc XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
}

/*
NewXClient
传入三个参数，服务发现实例 Discovery、负载均衡模式 SelectMode 以及协议选项 *Option
为了尽量地复用已经创建好的 Socket 连接，使用 clients 保存创建成功的 Client 实例
Close 方法在结束后，关闭已经建立的连接
*/
func NewXClient(d Discovery, mode SelectMode, opt *myGoRPC.Option) *XClient {
	return &XClient{
		d:       d,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*myGoRPC.Client),
	}
}

/*
dial
检查 xc.clients 是否有缓存的 Client
如果有，检查是否是可用状态，如果是则返回缓存的 Client，如果不可用，则从缓存中删除
上一步中若没有返回缓存的 Client，则说明需要创建新的 Client，缓存并返回
*/
func (xc *XClient) dial(rpcAddr string) (*myGoRPC.Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	client, ok := xc.clients[rpcAddr]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}
	if client == nil {
		var err error
		client, err = myGoRPC.XDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}
	return client, nil
}

func (xc *XClient) call(rpcAddr string, ctx context.Context, service, method string, args, reply interface{}) error {
	client, err := xc.dial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, service, method, args, reply)
}

func (xc *XClient) Call(ctx context.Context, service, method string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode)
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, service, method, args, reply)
}

/*
Broadcast
将请求广播到所有的服务实例
如果任意一个实例发生错误，则返回其中一个错误；
如果调用成功，则返回其中一个的结果。
 */
func (xc *XClient) Broadcast(ctx context.Context, service, method string, args, reply interface{}) error {
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error

	replyDone := (reply == nil)
	ctx, cancel := context.WithCancel(ctx)

	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				// todo: note
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := xc.call(rpcAddr, ctx, service, method, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel()
			}
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
