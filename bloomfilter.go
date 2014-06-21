package bloomfilter

// TODO probabilities, filled ratio

import (
  "bytes"
  "encoding/binary"
  "errors"
  "github.com/steakknife/hamming"
  "math"
  "math/rand"
  "time"
)

const (
  randSeedMagic int64 = 0x3f4a61e5b9c0278d
)

type Hashable interface {
  BloomFilterHash() uint64
}

type Filter struct {
  bits []uint64
  keys []uint64
  m    uint64 // number of bits the "bits" field should recognize
  n    uint64 // number of inserted elements
}

// m is the size of the bloom filter in bits, >= 2
//
// k is the number of randomly generated keys used (One-Time-Pad-inspired), >= 1
func New(m, k uint64) *Filter {
  if m <= 1 {
    panic("m (number of bits in the bloom filter) must be > 1")
  }
  if k == 0 {
    panic("k (number of keys uses in the bloom filter) must be > 0")
  }
  return &Filter{
    m:    m,
    n:    0,
    bits: make([]uint64, (m+63)>>6), // ceiling( m / 64 )
    keys: newKeys(k),
  }
}

// maxn is the maximum anticipated number of elements
func OptimalK(m, maxN uint64) uint64 {
  return uint64(math.Ceil(float64(m) * math.Ln2 / float64(maxN)))
}

// p is the desired false positive probability
// ceiling( - n * ln(p) / ln(2)**2 )
func OptimalM(maxN uint64, p float64) uint64 {
  return uint64(math.Ceil(-float64(maxN) * math.Log(p) / math.Ln2 * math.Ln2))
}

func newKeys(k uint64) (keys []uint64) {
  keys = make([]uint64, k)
  r := rand.New(rand.NewSource(time.Now().UnixNano() ^ randSeedMagic))
  for i := uint64(0); i < k; i++ {
    keys[i] = uint64(r.Uint32())<<32 | uint64(r.Uint32()) // 64-bit random number
  }
  return
}

// Hashable -> hashes
func (f Filter) hash(v Hashable) (hashes []uint64) {
  rawHash := v.BloomFilterHash()
  n := len(f.keys)
  hashes = make([]uint64, n, n)
  for i := 0; i < n; i++ {
    hashes[i] = rawHash ^ f.keys[i]
  }
  return
}

func (f Filter) IsCompatible(f2 Filter) bool {
  if f.M() != f2.M() || f.K() != f2.K() {
    return false
  }
  for i := range f.keys {
    if f.keys[i] != f2.keys[i] {
      return false
    }
  }
  return true
}

func (f Filter) Copy() (out *Filter) {
  out = &Filter{
    m:    f.m,
    n:    f.n,
    keys: make([]uint64, f.K()),
    bits: make([]uint64, (f.m+63)>>6),
  }
  copy(out.keys, f.keys)
  copy(out.bits, f.bits)
  return out
}

func (f Filter) Union(f2 Filter) (out *Filter, err error) {
  if !f.IsCompatible(f2) {
    err = errors.New("Cannot combine incompatible Bloom filters")
    return
  }
  out = f.Copy()
  for i, x := range f2.bits {
    out.bits[i] |= x
  }
  return
}

func (f Filter) M() uint64 {
  return f.m
}

func (f Filter) K() uint64 {
  return uint64(len(f.keys))
}

// Upper-bound of probability of false positives
//  (1 - exp(-k*(n+0.5)/(m-1))) ** k
func (f Filter) FalsePosititveProbability() float64 {
  return math.Pow(1.0-math.Exp(float64(-f.K())*(float64(f.N())+0.5)/float64(f.M()-1)), float64(f.K()))
}

// marshalled binary layout:
//
//   k
//   n
//   m
//   keys
//   bits
//
func (f Filter) MarshalBinary() (data []byte, err error) {
  k := f.K()

  size := binary.Size(k) + binary.Size(f.n) + binary.Size(f.m) + binary.Size(f.keys) + binary.Size(f.bits)
  data = make([]byte, 0, size)
  buf := bytes.NewBuffer(data)

  err = binary.Write(buf, binary.LittleEndian, k)
  if err != nil {
    return
  }

  err = binary.Write(buf, binary.LittleEndian, f.n)
  if err != nil {
    return
  }

  err = binary.Write(buf, binary.LittleEndian, f.m)
  if err != nil {
    return
  }

  err = binary.Write(buf, binary.LittleEndian, f.keys)
  if err != nil {
    return
  }

  err = binary.Write(buf, binary.LittleEndian, f.bits)
  if err != nil {
    return
  }

  data = buf.Bytes()
  return
}

func (f *Filter) UnmarshalBinary(data []byte) (err error) {
  var k uint32

  buf := bytes.NewBuffer(data)
  err = binary.Read(buf, binary.LittleEndian, k)
  if err != nil {
    return
  }

  err = binary.Read(buf, binary.LittleEndian, f.n)
  if err != nil {
    return
  }

  err = binary.Read(buf, binary.LittleEndian, f.m)
  if err != nil {
    return
  }

  f.keys = make([]uint64, k, k)
  err = binary.Read(buf, binary.LittleEndian, f.keys)
  if err != nil {
    return
  }

  f.bits = make([]uint64, f.n, f.n)
  err = binary.Read(buf, binary.LittleEndian, f.bits)
  if err != nil {
    return
  }

  return nil
}

func (f Filter) getBit(i uint64) bool {
  if i >= f.m {
    i %= f.m
  }
  return (f.bits[i>>6] >> uint(i&0x3f)) != 0
}

func (f *Filter) setBit(i uint64) {
  if i >= f.m {
    i %= f.m
  }
  f.bits[i>>6] |= 1 << uint(i&0x3f)
}

// how many elements have been inserted (actually, how many Add()s have been performed?)
func (f Filter) N() uint64 {
  return f.n
}

func (f *Filter) Add(v Hashable) {
  for _, k := range f.hash(v) {
    f.setBit(k)
  }
  f.n++
}

// exhaustive count # of 1's
func (f Filter) PreciseFilledRatio() float64 {
  ones := 0
  for _, b := range f.bits {
    ones += hamming.CountBitsUint64(b)
  }
  return float64(ones) / float64(f.M())
}

// false: definitely false
// true:  maybe true or false
func (f Filter) Contains(v Hashable) bool {
  for _, k := range f.hash(v) {
    if !f.getBit(k) {
      return false
    }
  }
  return true // maybe
}