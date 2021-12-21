package main

import (
	"encoding/json"
	"fmt"
	"log"
	"myGoRPC/codec"
	"myGoRPC/server"
	"net"
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
	addr := make(chan string)
	go startServer(addr)

	// simple client
	conn, _ := net.Dial("tcp", <-addr)
	defer func() { _ = conn.Close() }()

	time.Sleep(time.Second)
	_ = json.NewEncoder(conn).Encode(server.DefaultOption)
	cc := codec.NewGobCodec(conn)

	// send req, receive reply
	for i := 0; i < 8; i++ {
		h := &codec.Header{
			Service: "testService",
			Method:  "callFunc",
			Seq:     uint64(10000 + i),
		}
		_ = cc.Write(h, fmt.Sprintf("myGoRPC req %d", h.Seq))
		_ = cc.ReadHeader(h)
		var reply string
		_ = cc.ReadBody(&reply)
		log.Println("reply: ", reply)
	}
}
