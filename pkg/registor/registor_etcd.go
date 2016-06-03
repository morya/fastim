package registor

import (
	"fmt"
	"github.com/CodisLabs/codis/pkg/utils/log"
	"github.com/coreos/go-etcd/etcd"
	"github.com/fagongzi/fastim/pkg/model"
	"github.com/fagongzi/fastim/pkg/util"
	"strconv"
	"strings"
	"time"
)

type EtcdRegistor struct {
	prefix    string
	etcdAddrs []string
	cli       *etcd.Client

	pathOfRouters, pathOfRules, pathOfSupports string

	watchCh            chan *etcd.Response
	evtCh              chan *Evt
	watchMethodMapping map[EvtSrc]func(EvtType, *etcd.Response) *Evt
}

func NewEtcdRegistor(etcdAddrs []string, prefix string) Registor {
	e := &EtcdRegistor{
		prefix:    prefix,
		etcdAddrs: etcdAddrs,
		cli:       etcd.NewClient(etcdAddrs),

		pathOfRouters:  fmt.Sprintf("%s/routers", prefix),
		pathOfRules:    fmt.Sprintf("%s/rules", prefix),
		pathOfSupports: fmt.Sprintf("%s/supports", prefix),

		watchMethodMapping: make(map[EvtSrc]func(EvtType, *etcd.Response) *Evt),
	}

	e.init()

	return e
}

func (self EtcdRegistor) RegistorSupport(support *model.Support, ttl uint64) error {
	timer := time.NewTicker(time.Second * time.Duration(ttl))

	go func() {
		for {
			<-timer.C
			self.doRegistorSupport(support, ttl)
		}
	}()

	err := self.doRegistorSupport(support, ttl)

	if err != nil {
		return err
	}

	log.Infof("%s registry to <%s> success.", util.MODULE_REGISTRY, self.etcdAddrs)
	return nil
}

func (self EtcdRegistor) DeregistorSupport(support *model.Support) error {
	key := getSupportKey(self.pathOfSupports, support)

	_, err := self.cli.Delete(key, true)

	if err != nil {
		log.ErrorErrorf(err, "%s deregistry <%s> from <%s> failure.", util.MODULE_REGISTRY, key, self.etcdAddrs)
		return err
	}

	log.Infof("%s deregistry <%s> from <%s> success.", util.MODULE_REGISTRY, key, self.etcdAddrs)

	return nil
}

func (self EtcdRegistor) GetSupports() ([]*model.Support, error) {
	rsp, err := self.cli.Get(self.pathOfSupports, true, false)

	if nil != err {
		return nil, err
	}

	l := rsp.Node.Nodes.Len()
	supports := make([]*model.Support, l)

	for i := 0; i < l; i++ {
		n := rsp.Node.Nodes[i]

		if n.Dir {
			continue
		}
		supports[i] = model.UnMarshalSupport([]byte(n.Value))
	}

	return supports, nil
}

func (self EtcdRegistor) RegisterRouter(addr, protolcol string, ttl uint64) error {
	timer := time.NewTicker(time.Second * time.Duration(ttl))

	go func() {
		for {
			<-timer.C
			self.doRegistorRouter(addr, protolcol, ttl)
		}
	}()

	err := self.doRegistorRouter(addr, protolcol, ttl)

	if err != nil {
		return err
	}

	log.Infof("%s registry to <%s> success.", util.MODULE_REGISTRY, self.etcdAddrs)
	return nil
}

func (self EtcdRegistor) DeregisterRouter(addr, protolcol string) error {
	key := getRouterKey(self.pathOfRouters, addr, protolcol)
	_, err := self.cli.Delete(key, true)

	if err != nil {
		log.ErrorErrorf(err, "%s deregistry <%s> from <%s> failure.", util.MODULE_REGISTRY, key, self.etcdAddrs)
	}

	log.Infof("%s deregistry <%s> from <%s> success.", util.MODULE_REGISTRY, key, self.etcdAddrs)

	return err
}

func (self EtcdRegistor) Watch(evtCh chan *Evt, stopCh chan bool) error {
	self.watchCh = make(chan *etcd.Response)
	self.evtCh = evtCh

	log.Infof("%s watch at <%s> from <%s> success.", util.MODULE_REGISTRY, self.prefix, self.etcdAddrs)

	go self.doWatch()

	_, err := self.cli.Watch(self.prefix, 0, true, self.watchCh, stopCh)
	return err
}

func (self *EtcdRegistor) doWatch() {
	for {
		rsp := <-self.watchCh

		var evtSrc EvtSrc
		var evtType EvtType
		key := rsp.Node.Key

		if strings.HasPrefix(key, self.pathOfRouters) {
			evtSrc = EVT_SRC_ROUTER
		} else if strings.HasPrefix(key, self.pathOfSupports) {
			evtSrc = EVT_SRC_SUPPORT
		} else {
			continue
		}

		log.Debugf("%s registry changed, <%s, %s>.", util.MODULE_REGISTRY, rsp.Node.Key, rsp.Action)

		if rsp.Action == "set" {
			if rsp.PrevNode == nil {
				evtType = EVT_TYPE_NEW
			} else {
				evtType = EVT_TYPE_UPDATE
			}
		} else if rsp.Action == "create" {
			evtType = EVT_TYPE_NEW
		} else if rsp.Action == "delete" || rsp.Action == "expire" {
			evtType = EVT_TYPE_DELETE
		} else {
			continue
		}

		fn := self.watchMethodMapping[evtSrc]

		if nil != fn {
			self.evtCh <- self.watchMethodMapping[evtSrc](evtType, rsp)
		} else {
			log.Debugf("%s evt src<%+v>, type<%+v>, handler fun not found.", util.MODULE_REGISTRY, evtSrc, evtType)
		}
	}
}

func (self *EtcdRegistor) doWatchWithSupport(evtType EvtType, rsp *etcd.Response) *Evt {
	support := model.UnMarshalSupport([]byte(rsp.Node.Value))
	key := strings.Replace(rsp.Node.Key, fmt.Sprintf("%s/", self.pathOfSupports), "", 1)

	if support.Addr == "" {
		support.Product, support.Addr = parseSupportKey(key)
	}

	return &Evt{
		Src:   EVT_SRC_SUPPORT,
		Type:  evtType,
		Key:   key,
		Value: support,
	}
}

func (self *EtcdRegistor) doRegistorRouter(addr, protolcol string, ttl uint64) error {
	key := getRouterKey(self.pathOfRouters, addr, protolcol)

	_, err := self.cli.Set(key, "", ttl)

	if err != nil {
		log.ErrorErrorf(err, "%s registry <%s> to <%s> failure.", util.MODULE_REGISTRY, key, self.etcdAddrs)
	}

	log.Debugf("%s registry <%s> to <%s> success.", util.MODULE_REGISTRY, key, self.etcdAddrs)

	return err
}

func (self *EtcdRegistor) doRegistorSupport(support *model.Support, ttl uint64) error {
	key := getSupportKey(self.pathOfSupports, support)

	_, err := self.cli.Set(key, string(support.Marshal()), ttl)

	if err != nil {
		log.ErrorErrorf(err, "%s registry <%s> to <%s> failure.", util.MODULE_REGISTRY, key, self.etcdAddrs)
		return err
	}

	log.Debugf("%s registry <%s> to <%s> success.", util.MODULE_REGISTRY, key, self.etcdAddrs)

	return nil
}

func (self *EtcdRegistor) init() {
	self.watchMethodMapping[EVT_SRC_SUPPORT] = self.doWatchWithSupport
}

func getSupportKey(prefix string, support *model.Support) string {
	return fmt.Sprintf("%s/%d:%s", prefix, support.Product, util.ConvertToIp(support.Addr))
}

func parseSupportKey(key string) (int, string) {
	values := strings.Split(key, ":")
	product, _ := strconv.Atoi(values[0])
	return product, values[1]
}

func getRouterKey(prefix, addr, protolcol string) string {
	return fmt.Sprintf("%s/%s:%s", prefix, protolcol, util.ConvertToIp(addr))
}
