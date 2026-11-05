package mediadecode

import "encoding/binary"

// DecodeChannelsPrefix modifies only the first 128 KiB using the per-card
// decode key. It is a local byte transform, not a Channels network protocol.
func DecodeChannelsPrefix(data []byte, key uint64) {
	stream := newChannelsISAAC(key)
	limit := min(len(data), 128<<10)
	var block [8]byte
	for offset := 0; offset < limit; offset += 8 {
		binary.BigEndian.PutUint64(block[:], stream.random())
		for i := 0; i < 8 && offset+i < limit; i++ {
			data[offset+i] ^= block[i]
		}
	}
}

type channelsISAAC struct {
	count      uint64
	seed, mm   [256]uint64
	aa, bb, cc uint64
}

func newChannelsISAAC(key uint64) *channelsISAAC {
	c := &channelsISAAC{count: 255}
	c.seed[0] = key
	channelsInit(c)
	return c
}
func (c *channelsISAAC) random() uint64 {
	v := c.seed[c.count]
	if c.count == 0 {
		c.step()
		c.count = 255
	} else {
		c.count--
	}
	return v
}
func channelsInit(c *channelsISAAC) {
	const golden = uint64(0x9e3779b97f4a7c13)
	a, b, d, e, f, g, h, i := golden, golden, golden, golden, golden, golden, golden, golden
	for x := 0; x < 4; x++ {
		channelsMix(&a, &b, &d, &e, &f, &g, &h, &i)
	}
	for x := 0; x < 256; x += 8 {
		a += c.seed[x]
		b += c.seed[x+1]
		d += c.seed[x+2]
		e += c.seed[x+3]
		f += c.seed[x+4]
		g += c.seed[x+5]
		h += c.seed[x+6]
		i += c.seed[x+7]
		channelsMix(&a, &b, &d, &e, &f, &g, &h, &i)
		c.mm[x] = a
		c.mm[x+1] = b
		c.mm[x+2] = d
		c.mm[x+3] = e
		c.mm[x+4] = f
		c.mm[x+5] = g
		c.mm[x+6] = h
		c.mm[x+7] = i
	}
	for x := 0; x < 256; x += 8 {
		a += c.mm[x]
		b += c.mm[x+1]
		d += c.mm[x+2]
		e += c.mm[x+3]
		f += c.mm[x+4]
		g += c.mm[x+5]
		h += c.mm[x+6]
		i += c.mm[x+7]
		channelsMix(&a, &b, &d, &e, &f, &g, &h, &i)
		c.mm[x] = a
		c.mm[x+1] = b
		c.mm[x+2] = d
		c.mm[x+3] = e
		c.mm[x+4] = f
		c.mm[x+5] = g
		c.mm[x+6] = h
		c.mm[x+7] = i
	}
	c.step()
}
func (c *channelsISAAC) step() {
	c.cc++
	c.bb += c.cc
	for x := 0; x < 256; x++ {
		switch x % 4 {
		case 0:
			c.aa = ^(c.aa ^ (c.aa << 21))
		case 1:
			c.aa ^= c.aa >> 5
		case 2:
			c.aa ^= c.aa << 12
		case 3:
			c.aa ^= c.aa >> 33
		}
		c.aa += c.mm[(x+128)%256]
		old := c.mm[x]
		next := c.mm[(old>>3)%256] + c.aa + c.bb
		c.mm[x] = next
		c.bb = c.mm[(next>>11)%256] + old
		c.seed[x] = c.bb
	}
}
func channelsMix(a, b, c, d, e, f, g, h *uint64) {
	*a -= *e
	*f ^= *h >> 9
	*h += *a
	*b -= *f
	*g ^= *a << 9
	*a += *b
	*c -= *g
	*h ^= *b >> 23
	*b += *c
	*d -= *h
	*a ^= *c << 15
	*c += *d
	*e -= *a
	*b ^= *d >> 14
	*d += *e
	*f -= *b
	*c ^= *e << 20
	*e += *f
	*g -= *c
	*d ^= *f >> 17
	*f += *g
	*h -= *d
	*e ^= *g << 14
	*g += *h
}
