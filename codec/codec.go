package codec

import "io"

/*
Header

定义客户端发送的请求头信息
*/
type Header struct {
	Service string // 服务名
	Method  string // 方法名
	Seq     uint64 // 请求序列号
	Error   string // 错误信息
}

/*
Codec
定义接口，实现不同的实例
*/
type Codec interface {
	io.Closer // 关闭数据流
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

/*
NewCodecFunc

抽象出 Codec 的构造函数
客户端、服务端可以通过 Type 得到相应构造函数 （与工厂模式类似）
 */
type NewCodecFunc func(closer io.ReadWriteCloser) Codec

/*
Type
定义 Codec 类型，GobType, JsonType(未实现)
 */
type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json" // not implemented
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
