// Copyright 2019 smallnest. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package ringbuffer

import (
	"errors"
	"sync"
	"unsafe"
)

var (
	ErrTooManyDataToWrite = errors.New("too many data to write")
	ErrIsFull             = errors.New("ringbuffer is full")
	ErrIsEmpty            = errors.New("ringbuffer is empty")
)

// RingBuffer is a circular buffer that implement io.ReaderWriter interface.
type RingBuffer struct {
	buf    []byte
	size   int
	r      int // next position to read
	w      int // next position to write
	isFull bool
	mu     sync.Mutex
}

// New returns a new RingBuffer whose buffer has the given size.
func New(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Read reads up to len(p) bytes into p. It returns the number of bytes read (0 <= n <= len(p)) and any error encountered. Even if Read returns n < len(p), it may use all of p as scratch space during the call. If some data is available but not len(p) bytes, Read conventionally returns what is available instead of waiting for more.
// When Read encounters an error or end-of-file condition after successfully reading n > 0 bytes, it returns the number of bytes read. It may return the (non-nil) error from the same call or return the error (and n == 0) from a subsequent call.
// Callers should always process the n > 0 bytes returned before considering the error err. Doing so correctly handles I/O errors that happen after reading some bytes and also both of the allowed EOF behaviors.
func (r *RingBuffer) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	r.mu.Lock()
	// 判空，buffer 为空则返回 err empty
	if r.w == r.r && !r.isFull {
		r.mu.Unlock()
		return 0, ErrIsEmpty
	}

	if r.w > r.r {
		n = r.w - r.r
		if n > len(p) {
			n = len(p)
		}
		copy(p, r.buf[r.r:r.r+n])
		r.r = (r.r + n) % r.size
		r.mu.Unlock()
		return
	}

	n = r.size - r.r + r.w
	if n > len(p) {
		n = len(p)
	}

	if r.r+n <= r.size {
		copy(p, r.buf[r.r:r.r+n])
	} else {
		c1 := r.size - r.r
		copy(p, r.buf[r.r:r.size])
		c2 := n - c1
		copy(p[c1:], r.buf[0:c2])
	}
	r.r = (r.r + n) % r.size

	r.isFull = false
	r.mu.Unlock()
	return n, err
}

// ReadByte reads and returns the next byte from the input or ErrIsEmpty.
func (r *RingBuffer) ReadByte() (b byte, err error) {
	r.mu.Lock()
	if r.w == r.r && !r.isFull {
		r.mu.Unlock()
		return 0, ErrIsEmpty
	}
	b = r.buf[r.r]
	r.r++
	if r.r == r.size {
		r.r = 0
	}

	r.isFull = false
	r.mu.Unlock()
	return b, err
}

// Write writes len(p) bytes from p to the underlying buf.
// It returns the number of bytes written from p (0 <= n <= len(p)) and any error encountered that caused the write to stop early.
// Write returns a non-nil error if it returns n < len(p).
// Write must not modify the slice data, even temporarily.
func (r *RingBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	if r.isFull {
		r.mu.Unlock()
		return 0, ErrIsFull
	}

	// avail : buffer 中还剩多少 byte 可写
	var avail int
	if r.w >= r.r {
		avail = r.size - r.w + r.r
	} else {
		avail = r.r - r.w
	}

	// 如果一次性写入的 byte 数量大于 buffer 中剩余可用 bytes 数，则用 p 把 buffer 填满，p 中剩余未写入的 buffer 的 byte 数会通过 return n, error 来通知调用者
	if len(p) > avail {
		err = ErrTooManyDataToWrite
		p = p[:avail]
	}
	n = len(p)

	// w > r 说明这一次写入，可能导致 w 跨过起点0，继续写入。所以我们要分为  w -> 0，0 -> r 2次写入。
	// w < r 说明这一次写入，w 不会跨越 0，所以放心大胆直接 copy
	if r.w >= r.r {
		// c1 : w 距离终点的距离。
		// 如果 w 里终点的距离还有 10，当前要写入4个 byte，那就直接写入，然后 w 往终点走
		c1 := r.size - r.w
		if c1 >= n {
			copy(r.buf[r.w:], p)
			r.w += n
		// 如果 w 里终点的距离还有 10，当前要写入14个 byte。则把这11个byte 分为 {10 byte, 4byte} 两次写入。
		// 	1. 第一次写入前 10 个byte。 - copy(buff[w:], p[:10])
		// 	2. 第二次写入剩余 14-10(n-c1) = 4byte，  - copy(buff[0:], p[10:])
		} else {
			copy(r.buf[r.w:], p[:c1])
			c2 := n - c1
			copy(r.buf[0:], p[c1:])
			r.w = c2
		}
	} else {
		// 如果 w 落后于 r，直接写入到 buffer 里。 因为 w 写入 bytes 后，一旦碰到 r 就说明满了。  前面的代码已经确保了最多写到满为止。
		copy(r.buf[r.w:], p)
		r.w += n
	}

	// w 走完一圈，回到了起点，归零
	if r.w == r.size {
		r.w = 0
	}

	// 写入之后，如果 w 和 r 来到了同一个地方，则说明 buffer 满了
	if r.w == r.r {
		r.isFull = true
	}
	r.mu.Unlock()

	return n, err
}

// WriteByte writes one byte into buffer, and returns ErrIsFull if buffer is full.
// 当只需要写入 1 byte 时，用 WriteByte 更高效。
// 什么情况下需要写入 1byte 呢？ 因为bytes无边界，如果你想使用 \r 或 \t \n 之类的做为消息边界，就可以用 WriteByte
func (r *RingBuffer) WriteByte(c byte) error {
	r.mu.Lock()
	if r.w == r.r && r.isFull {
		r.mu.Unlock()
		return ErrIsFull
	}
	r.buf[r.w] = c
	r.w++

	if r.w == r.size {
		r.w = 0
	}
	if r.w == r.r {
		r.isFull = true
	}
	r.mu.Unlock()

	return nil
}

// Length return the length of available read bytes.
func (r *RingBuffer) Length() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.w == r.r {
		if r.isFull {
			return r.size
		}
		return 0
	}

	if r.w > r.r {
		return r.w - r.r
	}

	return r.size - r.r + r.w
}

// Capacity returns the size of the underlying buffer.
func (r *RingBuffer) Capacity() int {
	return r.size
}

// Free returns the length of available bytes to write.
// 返回 ringbuffer 里剩余可写的 bytes 数量。 只是方法名叫 Free 我开始以为是个动词，以为是释放了 ringbuffer 里的什么东西。
func (r *RingBuffer) Free() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 当 w 与 r 相遇时，ringbuffer 不为空则为满。
	if r.w == r.r {
		if r.isFull {
			return 0
		}
		return r.size
	}

	if r.w < r.r {
		return r.r - r.w
	}

	return r.size - r.w + r.r
}

// WriteString writes the contents of the string s to buffer, which accepts a slice of bytes.
func (r *RingBuffer) WriteString(s string) (n int, err error) {
	x := (*[2]uintptr)(unsafe.Pointer(&s))
	h := [3]uintptr{x[0], x[1], x[1]}
	buf := *(*[]byte)(unsafe.Pointer(&h))
	return r.Write(buf)
}

// Bytes returns all available read bytes. It does not move the read pointer and only copy the available data.
// 返回所有 bytes，但不是以 read 的方式，只是为了一窥当前 buffer 里的所有数据长什么样。
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	// buffer 为空返回 nil，为满则 TODO
	if r.w == r.r {
		if r.isFull {
			buf := make([]byte, r.size)
			copy(buf, r.buf[r.r:])
			copy(buf[r.size-r.r:], r.buf[:r.w])
			return buf
		}
		return nil
	}

	if r.w > r.r {
		buf := make([]byte, r.w-r.r)
		copy(buf, r.buf[r.r:r.w])
		return buf
	}

	n := r.size - r.r + r.w
	buf := make([]byte, n)

	if r.r+n < r.size {
		copy(buf, r.buf[r.r:r.r+n])
	} else {
		c1 := r.size - r.r
		copy(buf, r.buf[r.r:r.size])
		c2 := n - c1
		copy(buf[c1:], r.buf[0:c2])
	}

	return buf
}

// IsFull returns this ringbuffer is full.
func (r *RingBuffer) IsFull() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.isFull
}

// IsEmpty returns this ringbuffer is empty.
// r w 指针相遇，不为满则为空
func (r *RingBuffer) IsEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return !r.isFull && r.w == r.r
}

// Reset the read pointer and writer pointer to zero.
// r,w 归零归零归归零。
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.r = 0
	r.w = 0
	r.isFull = false
}
