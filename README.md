# myGoRPC

从零实现 Go 语言官方的标准库 net/rpc

# RPC简介

RPC(Remote Procedure Call)，是一种计算机通信协议，允许调用不同进程空间的程序，
客户端和服务器可以在相同机器上，也可以在不同机器上，程序调用时无需关注内部实现的细节。

基于 HTTP 协议的 Restful API 更通用，但是有以下缺点

- Restful API 接口需要额外定义
- 报文冗余
- RPC可以采用更高效的序列化协议，二进制传输
- RPC更灵活，更容易拓展和集成

RPC解决问题

- 传输协议 TCP / HTTP 
- 编码格式 JSON / XML / protobuf / ...
  - 编码解码
- 可用性问题
  - 超时
  - 异步
  - 并发
- 注册中心
- 负载均衡

# 基本通信过程

客户端与服务端的通信需要协商一些内容，例如 HTTP 报文，分为 header 和 body 两部分。
body 的格式和长度通过 header 中的 Content-Type 和 Content-Length 指定，
服务端通过解析 header 就能够知道如何从 body 中读取需要的信息。
对于 RPC 协议来说，这部分协商是需要自主设计的。

- 消息的编解码方式
  - 定义 Option 结构体
  - 一般来说，涉及协议协商的这部分信息，需要设计固定的字节来传输的。但是为了实现上更简单，采用 JSON 编码
  - 后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定
  - 服务端首先使用 JSON 解码 Option，然后通过 Option 的 CodeType 解码剩余的内容

即报文将以这样的形式发送：

```
| Option{RpcNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
| <------      固定 JSON 编码     ------>  | <-------   编码方式由 CodecType 决定   ------->|
```

在一次连接中，Option 固定在报文的最开始，Header 和 Body 可以有多个 即：

```
| Option{RpcNumber: xxx, CodecType: xxx} | Header{} | Body interface{} | Header{} | Body interface{} |
| <------      固定 JSON 编码    ------>  | <-------         编码方式由 CodecType 决定          ------->|
```

# Header

- 定义请求头 Header
  - 包含服务名、方法名、请求序列号、err

```
type Header struct {
	Service string // 服务名
	Method  string // 方法名
	Seq     uint64 // 请求序列号
	Error   string // 错误信息
}
```

# 消息编解码

- 对消息体进行编解码的接口 Codec
  - 抽象出 Codec 构造函数，客户端和服务端可以通过 Codec 的 Type 得到构造函数，从而创建 Codec 实例
  - io.Closer: 关闭数据流 
  - ReadHeader, ReadBody: 调用 gob.Decoder，从数据流中读取下一个值并写入（参数需要为相应类型的指针，nil 会丢弃数值）如果下一个值为 EOF，返回 io.EOF error 
  - Write: 调用 gob.Encoder 一次性写入数据到 header body 中
  - 定义一种 Codec - Gob

```
type Codec interface {
	io.Closer
	ReadHeader(header *Header) error
	ReadBody(body interface{}) error
	Write(header *Header, body interface{}) error
}
```

- GobCodec 结构体 实现了 Codec 接口
  - conn 是由构建函数传入，通常是通过 TCP 或者 Unix 建立 socket 时得到的链接实例
  - dec 和 enc 对应 gob 的 Decoder 和 Encoder
  - buf 是为了防止阻塞而创建的带缓冲的 Writer

如果不加消息编码，本质上是两个tcp的conn直接通信

```w -> conn -> conn -> r```

如果加上消息编码，就变成

```w -> bufio -> gob -> conn -> conn -> gob -> r```

针对conn生成了一个带缓存的写入，即：
先写入到 buffer 中, 再调用 buffer.Flush() 将 buffer 中的全部内容写入到 conn 中，提升写的效率

对于读则不需要这方面的考虑, 所以直接在 conn 中读内容即可

## 细节

1. 确认实现接口的所有方法

- `var _ Codec = (*GobCodec)(nil)` 确认 GobCodec 类型实现了 Codec 接口的所有方法
- 将空值转换为 *GobCodec 类型，再转换为 Codec 接口，如果转换失败，说明 GobCodec 并没有实现 Codec 接口的所有方法

# 服务端设计

- 首先定义了结构体 Server，没有任何的成员字段

``` 
type Server struct{}
```

- 实现了 Accept 方式，net.Listener 作为参数，for 循环等待 socket 连接建立，并开启子协程处理，处理过程交给了 ServerConn 方法
- ServeConn 的实现和之前讨论的通信过程紧密相关
  - 首先使用 json.NewDecoder 反序列化得到 Option 实例
  - 检查 RpcNumber 和 CodeType 的值是否正确
  - 然后根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec
- serveCodec 主要包含三个阶段 
  - 读取请求 readRequest 
  - 处理请求 handleRequest 
  - 回复请求 sendResponse
- 在一次连接中，允许接收多个请求，即多个 request header 和 request body，因此这里使用了 for 无限制地等待请求的到来，直到发生错误
  - handleRequest 使用了协程并发执行请求
  - 处理请求是并发的，但是回复请求的报文必须是逐个发送的，并发容易导致多个回复报文交织在一起，客户端无法解析。在这里使用锁(sending)保证 
  - 尽力而为，只有在 header 解析失败时，才终止循环

## 细节

1. 可能的粘包问题

- json 字符串是有数据的边界的， "{" 和 "}"
- /sdk/go1.16.4/src/encoding/json/stream.go:49 Decode()
- 每次反序列前会从conn中读取所有的数据到缓冲区中，再从缓冲区数据中读取一个完整的Json编码内容

2. server端解析Option的时候可能会破坏后面RPC消息的完整性

- 当客户端消息发送过快服务端消息积压时 （例：Option|Header|Body|Header|Body）
- 服务端使用json解析Option，json.Decode()调用conn.read()读取数据到内部的缓冲区（例：Option|Header）
- 此时后续的RPC消息就不完整了(Body|Header|Body)
- 初步使用 time.sleep() 方式隔离协议交换阶段与RPC消息阶段，减少这种问题发生的可能

3. 为什么sendResponse的时候还需要加锁？Go 里文件描述符(FD)的写入已经是线程安全的了

- 为了避免缓冲区 c.buf.Flush() 的时候，其他goroutine也在往同一个缓冲区写入，从而导致 err: short write的错误

## 当前总结

我们实现了一个消息的编解码器 GobCodec，
并且客户端与服务端实现了简单的协议交换(protocol exchange)，
即允许客户端使用不同的编码方式。
同时实现了服务端的雏形，建立连接，读取、处理并回复客户端的请求。

测试输出：

```
2021/12/21 13:45:34 start rpc server on  [::]:9999
2021/12/21 13:45:35 &{testService callFunc 10000 } myGoRPC req 10000
2021/12/21 13:45:35 reply:  myGoRPC response 10000
2021/12/21 13:45:35 &{testService callFunc 10001 } myGoRPC req 10001
2021/12/21 13:45:35 reply:  myGoRPC response 10001
2021/12/21 13:45:35 &{testService callFunc 10002 } myGoRPC req 10002
2021/12/21 13:45:35 reply:  myGoRPC response 10002
2021/12/21 13:45:35 &{testService callFunc 10003 } myGoRPC req 10003
2021/12/21 13:45:35 reply:  myGoRPC response 10003
```

# RPC Call

对于 `net/rpc` 来说，一个函数能被调用，需要满足形如 
`func (t *T) MethodName(argType T1, replyType *T2) error` 的以下条件

- the method’s type is exported. – 方法所属类型是导出的。 
- the method is exported. – 方式是导出的。 
- the method has two arguments, both exported (or builtin) types. – 两个入参，均为导出或内置类型。 
- the method’s second argument is a pointer. – 第二个入参必须是一个指针。 
- the method has return type error. – 返回值为 error 类型。

首先，需要封装结构体 Call 来承载一次 RPC 调用所需要的信息，
为了支持异步调用，添加了一个字段 Done，
Done 的类型是 chan *Call，当调用结束时，会调用 call.done() 通知调用方

``` 
type Call struct {
	Seq     uint64
	Service string
	Method  string
	Args    interface{}
	Reply   interface{}
	Error   error
	Done    chan *Call
}
```

# 客户端设计

``` 
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
```

创建 Client 实例

- 首先需要完成一开始的协议交换，即发送 Option 信息给服务端
- 协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应

客户端先需要实现和 Call 相关的三个方法

- registerCall 将参数 call 添加到 client.pending 中，并更新 client.seq
- removeCall 根据 seq，从 client.pending 中移除对应的 call，并返回
- terminateCalls 服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call

客户端需要实现 接受请求 receive()

- call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
- call 存在，但服务端处理出错，即 header.Error 不为空。
- call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值

还需要实现 Dial 函数，便于用户传入服务端地址，创建 Client 实例

暴露给用户的 RPC 服务调用接口 Go 和 Call

- Go 是一个异步接口，返回 call 实例
- Call 是对 Go 的封装，阻塞 call.Done，等待响应返回，是一个同步接口

## 细节

- 可选参数 
  - 形如 `func Printf(format string, a ...interface{})`
  - 可变参数使用 `name ...Type` 的形式声明在函数的参数列表中，而且需要是参数列表的最后一个参数
  - 从内部实现机理上来说，类型 `...type` 本质上是一个数组切片
  - 使用 interface{} 传递任意类型数据，switch 语句判定类型

``` 
func MyPrintf(args ...interface{}) {
    for _, arg := range args {
        switch arg.(type) {
            case int:
                fmt.Println(arg, "is an int value.")
            case string:
                fmt.Println(arg, "is a string value.")
            case int64:
                fmt.Println(arg, "is an int64 value.")
            default:
                fmt.Println(arg, "is an unknown type.")
        }
    }
}
```

## 当前总结

```
start rpc server on  [::]:9999
handleRequest header:  &{Foo Func 4 } args: {myGoRPC req 2} 
handleRequest header:  &{Foo Func 2 } args: {myGoRPC req 3} 
handleRequest header:  &{Foo Func 3 } args: {myGoRPC req 0} 
handleRequest header:  &{Foo Func 1 } args: {myGoRPC req 1} 
reply:  myGoRPC response 4
reply:  myGoRPC response 1
reply:  myGoRPC response 2
reply:  myGoRPC response 3
```

# 服务注册

RPC 框架的一个基础能力是：像调用本地程序一样调用远程服务。
对 Go 来说，这个问题就变成了如何将结构体的方法映射为服务。

通过反射，我们能够非常容易地获取某个结构体的所有方法，并且能够通过方法，获取到该方法所有的参数类型与返回值

例如 sync.WaitGroup ：

```
func main() {
	var wg sync.WaitGroup
	typ := reflect.TypeOf(&wg)
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		argv := make([]string, 0, method.Type.NumIn())
		returns := make([]string, 0, method.Type.NumOut())
		// j 从 1 开始，第 0 个入参是 wg 自己。
		for j := 1; j < method.Type.NumIn(); j++ {
			argv = append(argv, method.Type.In(j).Name())
		}
		for j := 0; j < method.Type.NumOut(); j++ {
			returns = append(returns, method.Type.Out(j).Name())
		}
		log.Printf("func (w *%s) %s(%s) %s",
			typ.Elem().Name(),
			method.Name,
			strings.Join(argv, ","),
			strings.Join(returns, ","))
    }
}
// 运行结果
func (w *WaitGroup) Add(int)
func (w *WaitGroup) Done()
func (w *WaitGroup) Wait()
```

## 定义结构体 MethodType

```
type MethodType struct {
	Method    reflect.Method // 方法本身
	ArgType   reflect.Type   // 入参类型
	ReplyType reflect.Type   // 返回类型
	NumCall   uint64         // 统计方法调用次数
}
```

我们还实现了 2 个方法 NewArgv 和 NewReplyv，用于创建对应类型的实例

## 定义结构体 Service

```
type Service struct {
    Name   string                 // 映射的结构体名称
    Typ    reflect.Type           // 结构体类型
    Rcvr   reflect.Value          // 结构体实例本身，调用时候作为第 0 个参数
    Method map[string]*MethodType // 存储所有符合条件的方法
}
```

完成构造函数 NewService，入参是任意需要映射为服务的结构体实例

```
func NewService(rcvr interface{}) *Service {
	s := new(Service)
	s.Rcvr = reflect.ValueOf(rcvr)
	s.Name = reflect.Indirect(s.Rcvr).Type().Name()
	s.Typ = reflect.TypeOf(rcvr)

	// ast Abstract Syntax Tree, 抽象语法树
	if !ast.IsExported(s.Name) {
		log.Fatalf("rpc server: %s is not a valid Service Name", s.Name)
	}
	s.RegisterMethods()
	return s
}
```

RegisterMethods 过滤出了符合条件的方法：
两个导出或内置类型的入参（反射时为 3 个，第 0 个是自身，类似于 python 的 self，java 中的 this）
返回值有且只有 1 个，类型为 error


还需要实现 Call 方法，即能够通过反射值调用方法

## 测试用例

为了保证 service 实现的正确性，为 service.go 写了几个测试用例

报错原因是go test会为指定的源码文件生成一个虚拟代码包——“command-line-arguments”，而 \_test.go引用了其他包中的数据并不属于代码包“command-line-arguments”，编译不通过，因此在go test的时候加上引用的包 `go test -v service_test.go service.go`


## 集成到服务端

通过反射结构体已经映射为服务，但请求的处理过程还没有完成。从接收到请求到回复还差以下几个步骤：

第一步，根据入参类型，将请求的 body 反序列化；

配套实现 findService 方法，即通过 ServiceMethod 从 serviceMap 中找到对应的 service
先在 serviceMap 中找到对应的 service 实例，
再从 service 实例的 method 中，找到对应的 methodType

补全 readRequest 方法
通过 newArgv() 和 newReplyv() 两个方法创建出两个入参实例，然后通过 cc.ReadBody() 将请求报文反序列化为第一个入参 argv，在这里同样需要注意 argv 可能是值类型，也可能是指针类型

第二步，调用 service.call，完成方法调用；

handleRequest 的实现非常简单，通过 req.svc.call 完成方法调用，将 replyv 传递给 sendResponse 完成序列化即可

第三步，将 reply 序列化为字节流，构造响应报文，返回。


## 当前总结

```
rpc server: register Foo.Sum
start rpc server on [::]:64244
1 + 1 = 2
3 + 9 = 12
4 + 16 = 20
2 + 4 = 6
0 + 0 = 0
```

# 超时处理

超时处理是 RPC 框架一个比较基本的能力，如果缺少超时处理机制，无论是服务端还是客户端都容易因为网络或其他错误导致挂死，资源耗尽，这些问题的出现大大地降低了服务的可用性。

纵观整个远程调用的过程，需要客户端处理超时的地方有：

* 与服务端建立连接，导致的超时
* 发送请求到服务端，写报文导致的超时
* 等待服务端处理时，等待处理导致的超时（比如服务端已挂死，迟迟不响应）
* 从服务端接收响应时，读报文导致的超时

需要服务端处理超时的地方有：

* 读取客户端请求报文时，读报文导致的超时
* 发送响应报文时，写报文导致的超时
* 调用映射服务的方法时，处理报文导致的超时

在 3 个地方添加超时处理机制。分别是：

1）客户端创建连接时  
2）客户端 `Client.Call()` 整个过程导致的超时（包含发送报文，等待处理，接收报文所有阶段）
3）服务端处理报文，即 `Server.handleRequest` 超时

## 创建链接超时

超时设定放在了 Option 中。ConnectTimeout 默认值为 10s，HandleTimeout 默认值为 0，即不设限。

实现了一个超时处理的外壳 `dialTimeout`，这个壳将 NewClient 作为入参，在 2 个地方添加了超时处理的机制

1. 将 `net.Dial` 替换为 `net.DialTimeout`，如果连接创建超时，将返回错误
2. 使用子协程执行 NewClient，执行完成后则通过信道 ch 发送结果，如果 `time.After()` 信道先接收到消息，则说明 NewClient 执行超时，返回错误

## Client.Call 超时

使用 context 包实现，控制权交给用户，控制更为灵活

可以使用 `context.WithTimeout` 创建具备超时检测能力的 context 对象来控制

```
    ctx, _ := context.WithTimeout(context.Background(), time.Second)
    var reply int
    err := client.Call(ctx, "Foo.Sum", &Args{1, 2}, &reply)
```

## 服务端处理超时

使用 `time.After()` 结合 `select+chan` 完成

这里需要确保 `sendResponse` 仅调用一次，因此将整个过程拆分为 `called` 和 `sent` 两个阶段，在这段代码中只会发生如下两种情况：

1.  called 信道接收到消息，代表处理没有超时，继续执行 sendResponse。
2.  `time.After()` 先于 called 接收到消息，说明处理已经超时，called 和 sent 都将被阻塞。在 `case <-time.After(timeout)` 处调用 `sendResponse`。

## 测试

连接超时、处理超时