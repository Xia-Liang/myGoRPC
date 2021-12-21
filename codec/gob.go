package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser // 构造函数传入，链接实例
	buf  *bufio.Writer      // 防止阻塞的带缓冲 Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}

/*
确保某个类型实现了某个接口的所有方法

*/
var _ Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	// 使用 buffer 来优化写入效率, 先写入到 buffer 中, 再调用 buffer.Flush() 来将 buffer 中的全部内容写入到 conn 中
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

func (g *GobCodec) Close() error {
	return g.conn.Close()
}

func (g *GobCodec) ReadHeader(header *Header) error {
	return g.dec.Decode(header)
}

func (g *GobCodec) ReadBody(body interface{}) error {
	return g.dec.Decode(body)
}

func (g *GobCodec) Write(header *Header, body interface{}) (err error) {
	defer func() {
		// 一次写入
		_ = g.buf.Flush()
		if err != nil {
			_ = g.Close()
		}
	}()
	// 如果 header body 写入错误，返回
	if err := g.enc.Encode(header); err != nil {
		log.Println("rpc codec.gob error encoding header:", err)
		return err
	}
	if err := g.enc.Encode(body); err != nil {
		log.Println("rpc codec.gob error encoding body:", err)
		return err
	}
	return nil
}
