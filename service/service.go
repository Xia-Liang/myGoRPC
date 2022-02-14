package service

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type MethodType struct {
	Method    reflect.Method // 方法本身
	ArgType   reflect.Type   // 入参类型
	ReplyType reflect.Type   // 返回类型
	NumCall   uint64         // 统计方法调用次数
}

func (m *MethodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.NumCall)
}

func (m *MethodType) NewArgv() reflect.Value {
	var argv reflect.Value

	// 指针类型和值类型创建实例的方式有细微区别
	if m.ArgType.Kind() == reflect.Ptr {
		// pointer type
		argv = reflect.New(m.ArgType.Elem())
	} else {
		// value type
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

func (m *MethodType) NewReplyv() reflect.Value {
	// reply must be a pointer type
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

type Service struct {
	Name   string                 // 映射的结构体名称
	Typ    reflect.Type           // 结构体类型
	Rcvr   reflect.Value          // 结构体实例本身，调用时候作为第 0 个参数
	Method map[string]*MethodType // 存储所有符合条件的方法
}

/*
NewService

Rcvr: 任意需要映射为服务的结构体实例
*/
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

/*
RegisterMethods

过滤出了符合条件的方法：
1. 两个导出或内置类型的入参
2. 返回值有且只有 1 个，类型为 error
*/
func (s *Service) RegisterMethods() {
	s.Method = make(map[string]*MethodType)

	for i := 0; i < s.Typ.NumMethod(); i++ {
		method := s.Typ.Method(i)
		mType := method.Type

		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}

		argType, replyType := mType.In(1), mType.In(2)

		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}

		s.Method[method.Name] = &MethodType{
			Method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.Name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

func (s *Service) Call(m *MethodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.NumCall, 1)
	f := m.Method.Func
	returnValues := f.Call([]reflect.Value{s.Rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
