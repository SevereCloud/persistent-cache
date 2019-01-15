package pcache

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

func readNextBytes(file *os.File, number int) ([]byte, error) {
	bytes := make([]byte, number)
	_, err := file.Read(bytes)
	return bytes, err
}

// Item struct
type Item struct {
	Object     interface{}
	Expiration int64
}

// Expired Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

// NoExpiration for use with functions that take an expiration time.
const NoExpiration time.Duration = -1

// DefaultExpiration for use with functions that take an expiration time. Equivalent to
// passing in the same expiration duration as was given to New() or
// NewFrom() when the cache was created (e.g. 5 minutes.)
const DefaultExpiration time.Duration = 0

// Cache struct
type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	defaultExpiration time.Duration
	items             map[string]Item
	mu                sync.RWMutex
	onEvicted         func(string, interface{})
	janitor           *janitor
	filename          string
	file              *os.File
}

// Add an item to the cache, replacing any existing item. If the duration is 0
// (DefaultExpiration), the cache's default expiration time is used. If it is -1
// (NoExpiration), the item never expires.
func (c *cache) Set(k string, x interface{}, d time.Duration) {
	// "Inlining" of set
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.mu.Lock()
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
	c.writeBinLog(true, k)
	// NOTE: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)
	c.mu.Unlock()
}

func (c *cache) set(k string, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
	c.writeBinLog(true, k)
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.
func (c *cache) SetDefault(k string, x interface{}) {
	c.Set(k, x, DefaultExpiration)
}

// Add an item to the cache only if an item doesn't already exist for the given
// key, or if the existing item has expired. Returns an error otherwise.
func (c *cache) Add(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	_, found := c.get(k)
	if found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s already exists", k)
	}
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.
func (c *cache) Replace(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	_, found := c.get(k)
	if !found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (interface{}, bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[k]
	if !found {
		c.mu.RUnlock()
		return nil, false
	}
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return nil, false
		}
	}
	c.mu.RUnlock()
	return item.Object, true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
func (c *cache) GetWithExpiration(k string) (interface{}, time.Time, bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[k]
	if !found {
		c.mu.RUnlock()
		return nil, time.Time{}, false
	}

	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return nil, time.Time{}, false
		}

		// Return the item and the expiration time
		c.mu.RUnlock()
		return item.Object, time.Unix(0, item.Expiration), true
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	c.mu.RUnlock()
	return item.Object, time.Time{}, true
}

func (c *cache) get(k string) (interface{}, bool) {
	item, found := c.items[k]
	if !found {
		return nil, false
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item.Object, true
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k string) {
	c.mu.Lock()
	v, evicted := c.delete(k)
	c.mu.Unlock()
	if evicted {
		c.onEvicted(k, v)
	}
}

func (c *cache) delete(k string) (interface{}, bool) {
	if v, found := c.items[k]; found {
		delete(c.items, k)
		c.writeBinLog(false, k)
		if c.onEvicted != nil {
			return v.Object, true
		}
	}
	return nil, false
}

type keyAndValue struct {
	key   string
	value interface{}
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	c.mu.Lock()
	for k, v := range c.items {
		// "Inlining" of expired
		if v.Expiration > 0 && now > v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}
	}
	c.mu.Unlock()
	for _, v := range evictedItems {
		c.onEvicted(v.key, v.value)
	}
}

// Sets an (optional) function that is called with the key and value when an
// item is evicted from the cache. (Including when it is deleted manually, but
// not when it is overwritten.) Set to nil to disable.
func (c *cache) OnEvicted(f func(string, interface{})) {
	c.mu.Lock()
	c.onEvicted = f
	c.mu.Unlock()
}

func (c *cache) writeBinLog(set bool, k string) (err error) {
	if c.filename != "" {
		var buff bytes.Buffer

		// Set
		setByte := []byte{0}
		if set {
			setByte = []byte{1}
		}
		buff.Write(setByte)

		// Key
		keyBytes := []byte(k)

		lenKeyBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(lenKeyBytes, uint32(len(keyBytes)))
		buff.Write(lenKeyBytes)

		buff.Write(keyBytes)

		if set {
			v := c.items[k]

			// Expiration
			// expirationBytes := make([]byte, 4)
			// binary.LittleEndian.PutUint32(expirationBytes, uint32(v.Expiration))
			// buff.Write(expirationBytes)

			// Item
			var serialize bytes.Buffer
			enc := gob.NewEncoder(&serialize)
			defer func() {
				if x := recover(); x != nil {
					err = fmt.Errorf("Error registering item types with Gob library")
				}
			}()

			gob.Register(v.Object)
			err := enc.Encode(&v)
			if err != nil {
				return err
			}

			itemBytes := serialize.Bytes()
			lenItemBytes := make([]byte, 4)
			binary.LittleEndian.PutUint32(lenItemBytes, uint32(len(itemBytes)))
			buff.Write(lenItemBytes)
			buff.Write(itemBytes)
		}
		_, err := c.file.Write(buff.Bytes())
		if err != nil {
			return err
		}
	}
	return nil
}

// RewriteBinLog overwrites the binlog file. Useful for optimization.
func (c *cache) RewriteBinLog() error {
	if c.filename != "" {
		c.mu.Lock()
		c.clearBinLog()
		for k := range c.items {
			c.writeBinLog(true, k)
		}
		c.mu.Unlock()
	}
	return nil
}

func (c *cache) loadBinLog(filename string) error {
	now := time.Now().UnixNano()

	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	for {
		// Set
		setBytes, err := readNextBytes(file, 1)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Key
		lenKeyBytes, err := readNextBytes(file, 4)
		if err != nil {
			return err
		}
		lenKey := int(binary.LittleEndian.Uint32(lenKeyBytes))

		keyBytes, err := readNextBytes(file, lenKey)
		if err != nil {
			return err
		}
		key := string(keyBytes)

		if setBytes[0] == uint8(0) {
			delete(c.items, key)
		} else {
			// Expiration
			// expirationBytes, err := readNextBytes(file, 4)
			// if err != nil {
			// 	return err
			// }
			// expiration := int64(binary.LittleEndian.Uint32(expirationBytes))

			// Item
			lenItemBytes, err := readNextBytes(file, 4)
			if err != nil {
				return err
			}
			lenItem := int(binary.LittleEndian.Uint32(lenItemBytes))

			itemBytes, err := readNextBytes(file, lenItem)
			if err != nil {
				return err
			}

			var unserialize bytes.Buffer
			unserialize.Write(itemBytes)
			dec := gob.NewDecoder(&unserialize)
			var v Item
			err = dec.Decode(&v)
			if err != nil {
				return err
			}

			if v.Expiration == 0 || now < v.Expiration {
				c.items[key] = v
			}
		}
	}

	return nil
}

func (c *cache) clearBinLog() (err error) {
	if c.filename != "" {
		err = c.CloseBinLog()
		if err != nil {
			return
		}
		c.file, err = os.Create(c.filename)
	}
	return
}

// CloseBinLog closes the binlog file.
func (c *cache) CloseBinLog() (err error) {
	err = c.file.Close()
	return
}

// Items copies all unexpired items in the cache into a new map and returns it.
func (c *cache) Items() map[string]Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]Item, len(c.items))
	now := time.Now().UnixNano()
	for k, v := range c.items {
		// "Inlining" of Expired
		if v.Expiration > 0 {
			if now > v.Expiration {
				continue
			}
		}
		m[k] = v
	}
	return m
}

// ItemCount returns the number of items in the cache. This may include items
// that have expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	return n
}

// Delete all items from the cache.
func (c *cache) Flush() {
	c.mu.Lock()
	c.items = map[string]Item{}
	c.clearBinLog()
	c.mu.Unlock()
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)
}

func newCache(de time.Duration, filename string, m map[string]Item) (c *cache, err error) {
	if de == 0 {
		de = -1
	}

	c = &cache{
		defaultExpiration: de,
		items:             m,
		filename:          filename,
	}
	if filename != "" {
		c.file, err = os.OpenFile(c.filename, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)
	}

	return
}

func newCacheWithJanitor(de time.Duration, ci time.Duration, filename string, m map[string]Item) (*Cache, error) {
	c, err := newCache(de, filename, m)
	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}
	if ci > 0 && err != nil {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C, err
}

// New return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func New(defaultExpiration, cleanupInterval time.Duration, filename string) (c *Cache, err error) {
	items := make(map[string]Item)

	c, err = newCacheWithJanitor(defaultExpiration, cleanupInterval, filename, items)
	if err != nil {
		return
	}

	err = c.clearBinLog()
	return
}

// NewFrom return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
//
// NewFrom() also accepts an items map which will serve as the underlying map
// for the cache. This is useful for starting from a deserialized cache
// (serialized using e.g. gob.Encode() on c.Items()), or passing in e.g.
// make(map[string]Item, 500) to improve startup performance when the cache
// is expected to reach a certain minimum size.
//
// Only the cache's methods synchronize access to this map, so it is not
// recommended to keep any references to the map around after creating a cache.
// If need be, the map can be accessed at a later point using c.Items() (subject
// to the same caveat.)
//
// Note regarding serialization: When using e.g. gob, make sure to
// gob.Register() the individual types stored in the cache before encoding a
// map retrieved with c.Items(), and to register those same types before
// decoding a blob containing an items map.
func NewFrom(defaultExpiration, cleanupInterval time.Duration, filename string, items map[string]Item) (c *Cache, err error) {
	c, err = newCacheWithJanitor(defaultExpiration, cleanupInterval, filename, items)
	if err != nil {
		return
	}

	err = c.RewriteBinLog()
	return
}

// Load return cache from file with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func Load(defaultExpiration, cleanupInterval time.Duration, filename string) (c *Cache, err error) {
	items := make(map[string]Item)

	c, err = newCacheWithJanitor(defaultExpiration, cleanupInterval, "", items)
	if err != nil {
		return
	}

	err = c.loadBinLog(filename)
	if err != nil {
		return
	}
	c.file, err = os.OpenFile(filename, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)
	c.filename = filename
	return
}
