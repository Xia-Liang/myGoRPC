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

# 项目架构构建

## 基本通信过程

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

## 消息编解码

- 定义请求头 Header 
  - 包含服务名、方法名、请求序列号、err
- 对消息体进行编解码的接口 Codec
  - 抽象出 Codec 构造函数，客户端和服务端可以通过 Codec 的 Type 得到构造函数，从而创建 Codec 实例
  - 定义一种 Codec - Gob 
- GobCodec 结构体
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

### 细节

1. 确认实现接口的所有方法

- `var _ Codec = (*GobCodec)(nil)` 确认 GobCodec 类型实现了 Codec 接口的所有方法
- 将空值转换为 *GobCodec 类型，再转换为 Codec 接口，如果转换失败，说明 GobCodec 并没有实现 Codec 接口的所有方法

## 服务端

- 首先定义了结构体 Server，没有任何的成员字段
- 实现了 Accept 方式，net.Listener 作为参数，for 循环等待 socket 连接建立，并开启子协程处理，处理过程交给了 ServerConn 方法
- ServeConn 的实现和之前讨论的通信过程紧密相关
  - 首先使用 json.NewDecoder 反序列化得到 Option 实例
  - 检查 RpcNumber 和 CodeType 的值是否正确
  - 然后根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec
- serveCodec 的过程非常简单。主要包含三个阶段 
  - 读取请求 readRequest 
  - 处理请求 handleRequest 
  - 回复请求 sendResponse
- 在一次连接中，允许接收多个请求，即多个 request header 和 request body，因此这里使用了 for 无限制地等待请求的到来，直到发生错误
  - handleRequest 使用了协程并发执行请求
  - 处理请求是并发的，但是回复请求的报文必须是逐个发送的，并发容易导致多个回复报文交织在一起，客户端无法解析。在这里使用锁(sending)保证 
  - 尽力而为，只有在 header 解析失败时，才终止循环

### 细节

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

### 当前总结

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


