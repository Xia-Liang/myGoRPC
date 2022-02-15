package main

import (
	"context"
	"log"
	"myGoRPC"
	"net"
	"net/http"
	"sync"
	"time"
)

func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go callDay5(addr)
	startServerDay5(addr)
}

// ---------------- day 3 -----------------------------------

type Foo int

type Args struct{ Num1, Num2 int }

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func startServerDay5(addr chan string) {
	var foo Foo
	newServer := myGoRPC.NewServer()
	newServer.Register(&foo)
	l, _ := net.Listen("tcp", ":9999")

	newServer.HandleHTTP()
	addr <- l.Addr().String()
	_ = http.Serve(l, nil)
}

func callDay5(addrCh chan string) {
	client, _ := myGoRPC.DialHTTP("tcp", <-addrCh)
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			if err := client.Call(context.Background(), "Foo", "Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}
