package main

import (
	"fmt"
	"log"
	"myGoRPC/client"
	"myGoRPC/server"
	"net"
	"sync"
	"time"
)

func startServer(addr chan string) {
	l, err := net.Listen("tcp", ":9999")
	if err != nil {
		log.Fatal("network error: ", err)
	}
	log.Println("start rpc server on ", l.Addr())

	addr <- l.Addr().String()
	newServer := server.NewServer()
	newServer.Accept(l)
}

/*
main

在 startServer 中使用了信道 addr，确保服务端端口监听成功，客户端再发起请求

客户端首先发送 Option 进行协议交换，
接下来发送消息头 h := &codec.Header{} 和消息体  req ${h.Seq}
最后解析服务端的响应 reply，并打印出来
 */
func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go startServer(addr)

	client, _ := client.Dial("tcp", <-addr)
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)

	var wg sync.WaitGroup

	// send req, receive reply
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf("args: {myGoRPC req %d} ", i)
			var reply string
			if err := client.Call("Foo", "Func", args, &reply); err != nil {
				log.Fatal("main, call service.method error: ", err)
			}
			log.Println("reply: ", reply)
		}(i)
	}
	wg.Wait()
}
