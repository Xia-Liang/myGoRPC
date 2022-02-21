package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

/*
GoRegistryDiscovery
嵌套了 MultiServersDiscovery，复用
registry 注册中心的地址
timeout 服务列表的过期时间
lastUpdate 是代表最后从注册中心更新服务列表的时间，默认 10s 过期，即 10s 之后，需要从注册中心更新新的列表
*/
type GoRegistryDiscovery struct {
	*MultiServerDiscovery
	registry   string
	timeout    time.Duration
	lastUpdate time.Time
}

const defaultUpdateTimeout = time.Second * 10

func NewGoRegistryDiscovery(registerAddr string, timeout time.Duration) *GoRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	return &GoRegistryDiscovery{
		MultiServerDiscovery: NewMultiServerDiscovery(make([]string, 0)),
		registry:             registerAddr,
		timeout:              timeout,
	}
}

func (d *GoRegistryDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

func (d *GoRegistryDiscovery) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry: refresh servers from registry ", d.registry)
	resp, err := http.Get(d.registry)
	if err != nil {
		log.Println("rpc registry refresh err: ", err)
		return err
	}
	servers := strings.Split(resp.Header.Get("GoRPC-Servers"), ",")
	d.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}

func (d *GoRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := d.Refresh(); err != nil {
		return "", err
	}
	return d.MultiServerDiscovery.Get(mode)
}

func (d *GoRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServerDiscovery.GetAll()
}
