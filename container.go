package dygo

import (
	"fmt"
	"reflect"
	"slices"
)

// instantiate builds every singleton provider and controller, module level by
// module level.
func (c *container) instantiate() error {
	for _, mi := range c.modulesByLevel() {
		for _, n := range slices.Concat(mi.providers, mi.controllers) {
			if n.scope() != Singleton {
				continue
			}
			if _, err := c.ensure(nil, n); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensure returns n's instance, building it if needed. Singletons are built
// once; transient providers on every call; request scoped providers once per
// request scope rs (nil outside a request).
func (c *container) ensure(rs *requestScope, n *node) (reflect.Value, error) {
	if n.p.kind == kindAlias {
		return c.ensure(rs, n.alias.target)
	}
	switch n.scope() {
	case Transient:
		return c.construct(rs, n)
	case RequestScoped:
		if rs != nil && n.durable() && rs.cid.ID != "" {
			if rs.durable != nil {
				return rs.durable.ensure(c, n)
			}
			return c.durable(rs.cid).ensure(c, n)
		}
		if rs != nil && rs.noRequest {
			return reflect.Value{}, fmt.Errorf("module %q: request scoped %s is not durable, so it cannot be resolved with dygo.WithDurableContext: %w",
				n.mod.name, n, errNoRequest)
		}
		return rs.ensure(c, n)
	}
	switch n.state {
	case stateBuilt:
		return n.instance, nil
	case stateBuilding:
		return reflect.Value{}, fmt.Errorf("module %q: %s was requested while it is being built; "+
			"call Lazy.Get or ModuleRef.Get after construction, for example in OnModuleInit", n.mod.name, n)
	}
	n.state = stateBuilding
	v, err := c.construct(rs, n)
	if err != nil {
		n.state = stateUnbuilt
		return reflect.Value{}, err
	}
	n.instance, n.state, n.order = v, stateBuilt, len(c.built)
	c.built = append(c.built, n)
	c.log.Debug("dygo: built provider", "module", n.mod.name, "type", typeName(n.out), "source", n.p.src)
	return v, nil
}

// construct calls a node's constructor, or returns its value.
func (c *container) construct(rs *requestScope, n *node) (v reflect.Value, err error) {
	if n.p.kind == kindValue {
		return n.p.value, nil
	}
	args := make([]reflect.Value, 0, len(n.params))
	for _, p := range n.params {
		if !p.isIn {
			a, err := c.depValue(rs, n, p.deps[0])
			if err != nil {
				return reflect.Value{}, err
			}
			args = append(args, a)
			continue
		}
		s := reflect.New(p.typ).Elem()
		for _, d := range p.deps {
			a, err := c.depValue(rs, n, d)
			if err != nil {
				return reflect.Value{}, err
			}
			s.Field(d.field).Set(a)
		}
		args = append(args, s)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("module %q: %s panicked: %v", n.mod.name, n, r)
		}
	}()
	out := n.p.fn.Call(args)
	if len(out) == 2 && !out[1].IsNil() {
		return reflect.Value{}, fmt.Errorf("module %q: constructing %s: %w", n.mod.name, n, out[1].Interface().(error))
	}
	return out[0], nil
}

// depValue produces the argument for one dependency.
func (c *container) depValue(rs *requestScope, n *node, d *dep) (reflect.Value, error) {
	switch d.kind {
	case depContext:
		if rs != nil && n.scope() == RequestScoped {
			return reflect.ValueOf(&rs.ctx).Elem(), nil
		}
		return reflect.ValueOf(&c.ctx).Elem(), nil
	case depRequest:
		if rs == nil {
			return reflect.Value{}, fmt.Errorf("module %q: %s needs *http.Request outside a request", n.mod.name, n)
		}
		return reflect.ValueOf(rs.req), nil
	case depModuleRef:
		return reflect.ValueOf(n.mod.ref), nil
	case depContextID:
		if rs == nil {
			return reflect.Value{}, fmt.Errorf("module %q: %s needs dygo.ContextID outside a request", n.mod.name, n)
		}
		return reflect.ValueOf(rs.cid), nil
	case depGroup:
		s := reflect.MakeSlice(d.typ, 0, len(d.members))
		for _, m := range d.members {
			v, err := c.ensure(rs, m)
			if err != nil {
				return reflect.Value{}, err
			}
			s = reflect.Append(s, v)
		}
		return s, nil
	case depScoped:
		sv := reflect.New(d.typ)
		sv.Interface().(scopedSetter).dygoSetScoped(&scopedRef{c: c, target: d.target})
		return sv.Elem(), nil
	case depLazy:
		lv := reflect.New(d.typ)
		target := d.target
		lv.Interface().(lazySetter).dygoSetLazy(&lazyRef{resolve: func() (any, error) {
			v, err := c.ensure(rs, target)
			if err != nil {
				return nil, err
			}
			return v.Interface(), nil
		}})
		return lv.Elem(), nil
	case depOptional:
		ov := reflect.New(d.typ)
		if d.target != nil {
			v, err := c.ensure(rs, d.target)
			if err != nil {
				return reflect.Value{}, err
			}
			ov.Interface().(optionalSetter).dygoSetOptional(v.Interface())
		}
		return ov.Elem(), nil
	default: // depPlain
		if d.target == nil {
			return reflect.Zero(d.typ), nil
		}
		v, err := c.ensure(rs, d.target)
		if err != nil {
			return reflect.Value{}, err
		}
		if v.Type() != d.typ {
			// Interface or named-type conversion; assignability was checked at registration.
			conv := reflect.New(d.typ).Elem()
			conv.Set(v)
			return conv, nil
		}
		return v, nil
	}
}

// get resolves k from module mi's point of view.
func (c *container) get(mi *moduleInfo, k tokenKey) (any, error) {
	n, err := c.visible(mi, k)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAmbiguous, err)
	}
	if n == nil {
		return nil, fmt.Errorf("%w: %s is not visible in module %q", ErrNotFound, k, mi.name)
	}
	return c.instanceOf(n)
}

func (c *container) instanceOf(n *node) (any, error) {
	v, err := c.ensure(nil, n)
	if err != nil {
		return nil, err
	}
	return v.Interface(), nil
}
