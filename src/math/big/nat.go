// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file implements unsigned multi-precision integers (natural
// numbers). They are the building blocks for the implementation
// of signed integers, rationals, and floating-point numbers.

package big

import (
	"math/bits"
	"math/rand"
	"sync"
)

// An unsigned integer x of the form
//
//   x = x[n-1]*_B^(n-1) + x[n-2]*_B^(n-2) + ... + x[1]*_B + x[0]
//
// with 0 <= x[i] < _B and 0 <= i < n is stored in a slice of length n,
// with the digits x[i] as the slice elements.
//
// A number is normalized if the slice contains no leading 0 digits.
// During arithmetic operations, denormalized values may occur but are
// always normalized before returning the final result. The normalized
// representation of 0 is the empty or nil slice (length = 0).
//
type nat []Word

var (
	natOne = nat{1}
	natTwo = nat{2}
	natTen = nat{10}
)

// Determines whether normalizing mul, expNN, etc operations
// allow variable-time computation.
// Normally true since normalizing operations are inherently vartime anyway,
// but sometimes useful to set to false for testing purposes.
const defaultVarTime = true

// czero returns 1 if w is zero and 0 otherwise, in constant time
func czero(w Word) Word {
	w = (w >> _W2) | (w & _M2)
	return (w - 1) >> (_W - 1)
}

// return 0 if z is zero and any nonzero value otherwise, in constant time
func (z nat) nonzero() (nz Word) {
	for _, zi := range z {
		nz |= zi
	}
	return
}

// return 1 if z is zero and 0 otherwise, in constant time
func (z nat) czero() Word {
	return czero(z.nonzero())
}

func (z nat) clear() {
	for i := range z {
		z[i] = 0
	}
}

// set z to x if v == 0 and to y if v == 1, in constant time
func (z nat) sel(x, y nat, v Word) {
	xmask := v - 1
	ymask := ^xmask
	for i := range z {
		z[i] = x[i]&xmask | y[i]&ymask
	}
}

// normalize z to exactly cap words, or to the minimum number if cap == 0.
// z must already be at least cap words long.
func (z nat) cnorm(zcap int) nat {
	i := len(z)
	switch {
	case zcap == 0: // normalize for variable-time operation
		for i > 0 && z[i-1] == 0 {
			i--
		}
	case i > zcap: // normalize for constant-time operation
		if z[zcap:].nonzero() != 0 {
			panic("constant-time result too large")
		}
		i = zcap
	case i < zcap:
		panic("constant-time result too small")
	}
	return z[0:i]
}

func (z nat) norm() nat {
	return z.cnorm(0)
}

func (z nat) normalized() bool {
	i := len(z)
	return i == 0 || z[i-1] != 0
}

func (z nat) cmake(n, zcap int) nat {
	l := max(n, zcap) // enforce capacity for constant-time operation
	if l <= cap(z) {
		if l > n { // make sure zcap padding is cleared out
			z[n:l].clear()
		}
		return z[:l] // reuse z
	}
	// Choosing a good value for e has significant performance impact
	// because it increases the chance that a value can be reused.
	const e = 4 // extra capacity
	return make(nat, l, l+e)
}

func (z nat) make(n int) nat {
	return z.cmake(n, 0)
}

func (z nat) csetWord(x Word, zcap int) nat {
	z = z.cmake(1, zcap)
	z[0] = x
	return z.cnorm(zcap)
}

func (z nat) setWord(x Word) nat {
	return z.csetWord(x, 0)
}

func (z nat) csetUint64(x uint64, zcap int) nat {
	// single-word value
	if w := Word(x); uint64(w) == x {
		return z.csetWord(w, zcap)
	}
	// 2-word value
	z = z.cmake(2, zcap)
	z[1] = Word(x >> 32)
	z[0] = Word(x)
	// (note: the fact that this norm was missing before was a bug...)
	return z.cnorm(zcap)
}

func (z nat) setUint64(x uint64) nat {
	return z.csetUint64(x, 0)
}

func (z nat) cset(x nat, zcap int) nat {
	z = z.cmake(len(x), zcap)
	copy(z, x)
	return z
}

func (z nat) set(x nat) nat {
	return z.cset(x, 0)
}

func (z nat) cadd(x, y nat, zcap int) nat {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		return z.cadd(y, x, zcap)
	case m == 0:
		// n == 0 because m >= n; result is 0
		return z[:0]
	case n == 0:
		// result is x
		return z.cset(x, zcap)
	}
	// m > 0

	z = z.cmake(m+1, zcap)
	c := addVV(z[0:n], x, y)
	if m > n {
		c = addVW(z[n:m], x[n:], c)
	}
	z[m] = c

	return z.cnorm(zcap)
}

func (z nat) add(x, y nat) nat {
	return z.cadd(x, y, 0)
}

func (z nat) csub(x, y nat, zcap int) nat {
	m := len(x)
	n := len(y)

	var c Word
	switch {
	case m < n:
		// might not be underflow if y is unnormalized
		// XXX create test for this case
		z = z.cmake(m, zcap)
		c = subVV(z[0:m], x, y)
		c |= 1 ^ y[m:].czero()
	case m == 0:
		// n == 0 because m >= n; result is 0
		return z[:0]
	case n == 0:
		// result is x
		return z.cset(x, zcap)
	case m > n:
		z = z.cmake(m, zcap)
		c = subVV(z[0:n], x, y)
		c = subVW(z[n:], x[n:], c)
	default: // m == n
		z = z.cmake(m, zcap)
		c = subVV(z[0:m], x, y)
	}
	if c != 0 {
		panic("underflow")
	}

	return z.cnorm(zcap)
}

func (z nat) sub(x, y nat) nat {
	return z.csub(x, y, 0)
}

func (x nat) cmp(y nat) (r int) {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		return -y.cmp(x)
	case m == 0:
		// n == 0 because m >= n; result is 0 (equal)
		return 0
	}
	// m > 0

	lt, ne := cmpVV_g(x[0:n], y)
	if m > n {
		lt, ne = cmpVW_g(x[n:], lt, ne)
	}
	gt := 1 - (lt | czero(ne))

	return int(gt) - int(lt)
}

func (z nat) cmulAddWW(x nat, y, r Word, zcap int) nat {
	m := len(x)
	if m == 0 || y == 0 {
		return z.csetWord(r, zcap) // result is r
	}
	// m > 0

	z = z.cmake(m+1, zcap)
	z[m] = mulAddVWW(z[0:m], x, y, r)

	return z.cnorm(zcap)
}

func (z nat) mulAddWW(x nat, y, r Word) nat {
	return z.cmulAddWW(x, y, r, 0)
}

// basicMul multiplies x and y and leaves the result in z.
// The (non-normalized) result is placed in z[0 : len(x) + len(y)].
func basicMul(z, x, y nat, zcap int) {
	z[0 : len(x)+len(y)].clear() // initialize z
	for i, d := range y {
		// Optimize multiplies of zero words only if vartime allowed.
		// Assumes compiled code branches on zcap > 0 either before or
		// atomically with d != 0: does Go guarantee standard
		// evaluation order for || or is there a way to force it?
		if zcap > 0 || d != 0 {
			z[len(x)+i] = addMulVVW(z[i:i+len(x)], x, d)
		}
	}
}

// montgomery computes z mod m = x*y*2**(-n*_W) mod m,
// assuming k = -1/m mod 2**_W.
// z is used for storing the result which is returned;
// z must not alias x, y or m.
// See Gueron, "Efficient Software Implementations of Modular Exponentiation".
// https://eprint.iacr.org/2011/239.pdf
// In the terminology of that paper, this is an "Almost Montgomery Multiplication":
// x and y are required to satisfy 0 <= z < 2**(n*_W) and then the result
// z is guaranteed to satisfy 0 <= z < 2**(n*_W), but it may not be < m.
// For constant-time operation, zt must be a temporary nat the size of z.
func (z nat) montgomery(x, y, m nat, k Word, n int, zt nat, zcap int) nat {
	// This code assumes x, y, m are all the same length, n.
	// (required by addMulVVW and the for loop).
	// It also assumes that x, y are already reduced mod m,
	// or else the result will not be properly reduced.
	if len(x) != n || len(y) != n || len(m) != n {
		panic("math/big: mismatched montgomery number lengths")
	}
	z = z.cmake(n, zcap)
	z.clear()
	var c Word
	for i := 0; i < n; i++ {
		d := y[i]
		c2 := addMulVVW(z, x, d)
		t := z[0] * k
		c3 := addMulVVW(z, m, t)
		copy(z, z[1:])
		cx := c + c2
		cy := cx + c3
		z[n-1] = cy
		// see "Hacker's Delight", section 2-12 (overflow detection)
		c = (c&c2 | (c|c2)&^cx) >> (_W - 1)
		c |= (cx&c3 | (cx|c3)&^cy) >> (_W - 1)
	}
	if zt == nil { // variable-time operation
		if c != 0 {
			subVV(z, z, m)
		}
	} else { // constant-time operation
		subVV(zt, z, m)
		z.sel(z, zt, c)
	}
	return z
}

// Fast version of z[0:n+n>>1].add(z[0:n+n>>1], x[0:n]) w/o bounds checks.
// Factored out for readability - do not use outside karatsuba.
func karatsubaAdd(z, x nat, n int, zcap int) {
	if c := addVV(z[0:n], z, x); zcap > 0 || c != 0 {
		addVW(z[n:n+n>>1], z[n:], c)
	}
}

// Like karatsubaAdd, but does subtract.
func karatsubaSub(z, x nat, n int, zcap int) {
	if c := subVV(z[0:n], z, x); zcap > 0 || c != 0 {
		subVW(z[n:n+n>>1], z[n:], c)
	}
}

// Operands that are shorter than karatsubaThreshold are multiplied using
// "grade school" multiplication; for longer operands the Karatsuba algorithm
// is used.
var karatsubaThreshold int = 40 // computed by calibrate.go

// karatsuba multiplies x and y and leaves the result in z.
// Both x and y must have the same length n and n must be a
// power of 2. The result vector z must have len(z) >= 6*n.
// The (non-normalized) result is placed in z[0 : 2*n].
func karatsuba(z, x, y nat, zcap int) {
	n := len(y)

	// Switch to basic multiplication if numbers are odd or small.
	// (n is always even if karatsubaThreshold is even, but be
	// conservative)
	if n&1 != 0 || n < karatsubaThreshold || n < 2 {
		basicMul(z, x, y, zcap)
		return
	}
	// n&1 == 0 && n >= karatsubaThreshold && n >= 2

	// Karatsuba multiplication is based on the observation that
	// for two numbers x and y with:
	//
	//   x = x1*b + x0
	//   y = y1*b + y0
	//
	// the product x*y can be obtained with 3 products z2, z1, z0
	// instead of 4:
	//
	//   x*y = x1*y1*b*b + (x1*y0 + x0*y1)*b + x0*y0
	//       =    z2*b*b +              z1*b +    z0
	//
	// with:
	//
	//   xd = x1 - x0
	//   yd = y0 - y1
	//
	//   z1 =      xd*yd                    + z2 + z0
	//      = (x1-x0)*(y0 - y1)             + z2 + z0
	//      = x1*y0 - x1*y1 - x0*y0 + x0*y1 + z2 + z0
	//      = x1*y0 -    z2 -    z0 + x0*y1 + z2 + z0
	//      = x1*y0                 + x0*y1

	// split x, y into "digits"
	n2 := n >> 1              // n2 >= 1
	x1, x0 := x[n2:], x[0:n2] // x = x1*b + y0
	y1, y0 := y[n2:], y[0:n2] // y = y1*b + y0

	// z is used for the result and temporary storage:
	//
	//   6*n     5*n     4*n     3*n     2*n     1*n     0*n
	// z = [z2 copy|z0 copy| xd*yd | yd:xd | x1*y1 | x0*y0 ]
	//
	// For each recursive call of karatsuba, an unused slice of
	// z is passed in that has (at least) half the length of the
	// caller's z.

	// compute z0 and z2 with the result "in place" in z
	karatsuba(z, x0, y0, zcap)     // z0 = x0*y0
	karatsuba(z[n:], x1, y1, zcap) // z2 = x1*y1

	// compute xd (or the negative value if underflow occurs)
	neg := Word(0) // whether product xd*yd is negative
	xd := z[2*n : 2*n+n2]
	c := subVV(xd, x1, x0) // x1-x0
	if zcap > 0 {          // constant-time operation
		xt := z[3*n : 3*n+n2] // temporary for selection
		subVV(xt, x0, x1)     // x0-x1
		xd.sel(xd, xt, c)
	} else if c != 0 { // variable-time operation
		subVV(xd, x0, x1) // x0-x1
	}
	neg ^= c

	// compute yd (or the negative value if underflow occurs)
	yd := z[2*n+n2 : 3*n]
	c = subVV(yd, y0, y1) // y0-y1
	if zcap > 0 {         // constant-time operation
		yt := z[3*n : 3*n+n2]
		subVV(yt, y1, y0) // y1-y0
		yd.sel(yd, yt, c)
	} else if c != 0 { // variable-time operation
		subVV(yd, y1, y0) // y1-y0
	}
	neg ^= c

	// p = (x1-x0)*(y0-y1) == x1*y0 - x1*y1 - x0*y0 + x0*y1 for s > 0
	// p = (x0-x1)*(y0-y1) == x0*y0 - x0*y1 - x1*y0 + x1*y1 for s < 0
	p := z[n*3:]
	karatsuba(p, xd, yd, zcap)

	// save original z2:z0
	// (ok to use upper half of z since we're done recursing)
	r := z[n*4:]
	copy(r, z[:n*2])

	// add up all partial products
	//
	//   2*n     n     0
	// z = [ z2  | z0  ]
	//   +    [ z0  ]
	//   +    [ z2  ]
	//   +    [  p  ]
	//
	zn2 := z[n2 : n*2]
	karatsubaAdd(zn2, r, n, zcap)
	karatsubaAdd(zn2, r[n:], n, zcap)
	if zcap > 0 { // constant-time operation
		copy(r, zn2) // reuse r again as a temporary for selection
		karatsubaAdd(zn2, p, n, zcap)
		karatsubaSub(r, p, n, zcap)
		zn2.sel(zn2, r, neg)
	} else if neg == 0 {
		karatsubaAdd(zn2, p, n, zcap)
	} else {
		karatsubaSub(zn2, p, n, zcap)
	}
}

// alias reports whether x and y share the same base array.
func alias(x, y nat) bool {
	return cap(x) > 0 && cap(y) > 0 && &x[0:cap(x)][cap(x)-1] == &y[0:cap(y)][cap(y)-1]
}

// addAt implements z += x<<(_W*i); z must be long enough.
// (we don't use nat.add because we need z to stay the same
// slice, and we don't need to normalize z after each addition)
func addAt(z, x nat, i int, zcap int) {
	if n := len(x); n > 0 {
		if c := addVV(z[i:i+n], z[i:], x); zcap > 0 || c != 0 {
			j := i + n
			if j < len(z) {
				addVW(z[j:], z[j:], c)
			}
		}
	}
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// karatsubaLen computes an approximation to the maximum k <= n such that
// k = p<<i for a number p <= karatsubaThreshold and an i >= 0. Thus, the
// result is the largest number that can be divided repeatedly by 2 before
// becoming about the value of karatsubaThreshold.
func karatsubaLen(n int) int {
	i := uint(0)
	for n > karatsubaThreshold {
		n >>= 1
		i++
	}
	return n << i
}

func (z nat) cmul(x, y nat, zcap int) nat {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		return z.cmul(y, x, zcap)
	case m == 0 || n == 0:
		return z[:0].cnorm(zcap)
	case n == 1:
		return z.cmulAddWW(x, y[0], 0, zcap)
	}
	// m >= n > 1

	// determine if z can be reused
	if alias(z, x) || alias(z, y) {
		z = nil // z is an alias for x or y - cannot reuse
	}

	// use basic multiplication if the numbers are small
	if n < karatsubaThreshold {
		z = z.cmake(m+n, zcap)
		basicMul(z, x, y, zcap)
		return z.cnorm(zcap)
	}
	// m >= n && n >= karatsubaThreshold && n >= 2

	// determine Karatsuba length k such that
	//
	//   x = xh*b + x0  (0 <= x0 < b)
	//   y = yh*b + y0  (0 <= y0 < b)
	//   b = 1<<(_W*k)  ("base" of digits xi, yi)
	//
	k := karatsubaLen(n)
	// k <= n

	// multiply x0 and y0 via Karatsuba
	x0 := x[0:k]                     // x0 is not normalized
	y0 := y[0:k]                     // y0 is not normalized
	z = z.cmake(max(6*k, m+n), zcap) // enough space for karatsuba of x0*y0 and full result of x*y
	karatsuba(z, x0, y0, zcap)
	z = z[0 : m+n]  // z has final length but may be incomplete
	z[2*k:].clear() // upper portion of z is garbage (and 2*k <= m+n since k <= n <= m)

	// If xh != 0 or yh != 0, add the missing terms to z. For
	//
	//   xh = xi*b^i + ... + x2*b^2 + x1*b (0 <= xi < b)
	//   yh =                         y1*b (0 <= y1 < b)
	//
	// the missing terms are
	//
	//   x0*y1*b and xi*y0*b^i, xi*y1*b^(i+1) for i > 0
	//
	// since all the yi for i > 1 are 0 by choice of k: If any of them
	// were > 0, then yh >= b^2 and thus y >= b^2. Then k' = k*2 would
	// be a larger valid threshold contradicting the assumption about k.
	//
	if k < n || m != n {
		var t nat

		// add x0*y1*b
		if zcap == 0 {
			x0 = x0.norm()
		}
		y1 := y[k:]       // y1 is normalized because y is
		t = t.mul(x0, y1) // update t so we don't lose t's underlying array
		addAt(z, t, k, zcap)

		// add xi*y0<<i, xi*y1*b<<(i+k)
		if zcap == 0 {
			y0 = y0.norm()
		}
		for i := k; i < len(x); i += k {
			xi := x[i:]
			if len(xi) > k {
				xi = xi[:k]
			}
			if zcap == 0 {
				xi = xi.norm()
			}
			t = t.mul(xi, y0)
			addAt(z, t, i, zcap)
			t = t.mul(xi, y1)
			addAt(z, t, i+k, zcap)
		}
	}

	return z.cnorm(zcap)
}

func (z nat) mul(x, y nat) nat {
	return z.cmul(x, y, 0)
}

// mulRange computes the product of all the unsigned integers in the
// range [a, b] inclusively. If a > b (empty range), the result is 1.
func (z nat) mulRange(a, b uint64) nat {
	switch {
	case a == 0:
		// cut long ranges short (optimization)
		return z.setUint64(0)
	case a > b:
		return z.setUint64(1)
	case a == b:
		return z.setUint64(a)
	case a+1 == b:
		return z.mul(nat(nil).setUint64(a),
			nat(nil).setUint64(b))
	}
	m := (a + b) / 2
	return z.mul(nat(nil).mulRange(a, m), nat(nil).mulRange(m+1, b))
}

// q = (x-r)/y, with 0 <= r < y
func (z nat) divW(x nat, y Word) (q nat, r Word) {
	m := len(x)
	switch {
	case y == 0:
		panic("division by zero")
	case y == 1:
		q = z.set(x) // result is x
		return
	case m == 0:
		q = z[:0] // result is 0
		return
	}
	// m > 0
	z = z.make(m)
	r = divWVW(z, 0, x, y)
	q = z.norm()
	return
}

func (z nat) div(z2, u, v nat) (q, r nat) {
	if len(v) == 0 {
		panic("division by zero")
	}

	if u.cmp(v) < 0 {
		q = z[:0]
		r = z2.set(u)
		return
	}

	if len(v) == 1 {
		var r2 Word
		q, r2 = z.divW(u, v[0])
		r = z2.setWord(r2)
		return
	}

	q, r = z.divLarge(z2, u, v)
	return
}

// getNat returns a *nat of len n. The contents may not be zero.
// The pool holds *nat to avoid allocation when converting to interface{}.
func getNat(n int) *nat {
	var z *nat
	if v := natPool.Get(); v != nil {
		z = v.(*nat)
	}
	if z == nil {
		z = new(nat)
	}
	*z = z.make(n)
	return z
}

func putNat(x *nat) {
	natPool.Put(x)
}

var natPool sync.Pool

// q = (uIn-r)/v, with 0 <= r < y
// Uses z as storage for q, and u as storage for r if possible.
// See Knuth, Volume 2, section 4.3.1, Algorithm D.
// Preconditions:
//    len(v) >= 2
//    len(uIn) >= len(v)
func (z nat) divLarge(u, uIn, v nat) (q, r nat) {
	n := len(v)
	m := len(uIn) - n

	// determine if z can be reused
	// TODO(gri) should find a better solution - this if statement
	//           is very costly (see e.g. time pidigits -s -n 10000)
	if alias(z, uIn) || alias(z, v) {
		z = nil // z is an alias for uIn or v - cannot reuse
	}
	q = z.make(m + 1)

	qhatvp := getNat(n + 1)
	qhatv := *qhatvp
	if alias(u, uIn) || alias(u, v) {
		u = nil // u is an alias for uIn or v - cannot reuse
	}
	u = u.make(len(uIn) + 1)
	u.clear() // TODO(gri) no need to clear if we allocated a new u

	// D1.
	var v1p *nat
	shift := nlz(v[n-1])
	if shift > 0 {
		// do not modify v, it may be used by another goroutine simultaneously
		v1p = getNat(n)
		v1 := *v1p
		shlVU(v1, v, shift)
		v = v1
	}
	u[len(uIn)] = shlVU(u[0:len(uIn)], uIn, shift)

	// D2.
	vn1 := v[n-1]
	for j := m; j >= 0; j-- {
		// D3.
		qhat := Word(_M)
		if ujn := u[j+n]; ujn != vn1 {
			var rhat Word
			qhat, rhat = divWW(ujn, u[j+n-1], vn1)

			// x1 | x2 = q̂v_{n-2}
			vn2 := v[n-2]
			x1, x2 := mulWW(qhat, vn2)
			// test if q̂v_{n-2} > br̂ + u_{j+n-2}
			ujn2 := u[j+n-2]
			for greaterThan(x1, x2, rhat, ujn2) {
				qhat--
				prevRhat := rhat
				rhat += vn1
				// v[n-1] >= 0, so this tests for overflow.
				if rhat < prevRhat {
					break
				}
				x1, x2 = mulWW(qhat, vn2)
			}
		}

		// D4.
		qhatv[n] = mulAddVWW(qhatv[0:n], v, qhat, 0)

		c := subVV(u[j:j+len(qhatv)], u[j:], qhatv)
		if c != 0 {
			c := addVV(u[j:j+n], u[j:], v)
			u[j+n] += c
			qhat--
		}

		q[j] = qhat
	}
	if v1p != nil {
		putNat(v1p)
	}
	putNat(qhatvp)

	q = q.norm()
	shrVU(u, u, shift)
	r = u.norm()

	return q, r
}

// Length of x in bits. x need not be normalized.
func (x nat) bitLen() int {
	for i := len(x) - 1; i >= 0; i-- {
		if xi := x[i]; xi != 0 {
			return i*_W + bits.Len(uint(xi))
		}
	}
	return 0
}

// trailingZeroBits returns the number of consecutive least significant zero
// bits of x.
func (x nat) trailingZeroBits() uint {
	if len(x) == 0 {
		return 0
	}
	var i uint
	for x[i] == 0 {
		i++
	}
	// x[i] != 0
	return i*_W + uint(bits.TrailingZeros(uint(x[i])))
}

// z = x << s
func (z nat) shl(x nat, s uint) nat {
	m := len(x)
	if m == 0 {
		return z[:0]
	}
	// m > 0

	n := m + int(s/_W)
	z = z.make(n + 1)
	z[n] = shlVU(z[n-m:n], x, s%_W)
	z[0 : n-m].clear()

	return z.norm()
}

// z = x >> s
func (z nat) shr(x nat, s uint) nat {
	m := len(x)
	n := m - int(s/_W)
	if n <= 0 {
		return z[:0]
	}
	// n > 0

	z = z.make(n)
	shrVU(z, x[m-n:], s%_W)

	return z.norm()
}

func (z nat) setBit(x nat, i uint, b uint) nat {
	j := int(i / _W)
	m := Word(1) << (i % _W)
	n := len(x)
	switch b {
	case 0:
		z = z.make(n)
		copy(z, x)
		if j >= n {
			// no need to grow
			return z
		}
		z[j] &^= m
		return z.norm()
	case 1:
		if j >= n {
			z = z.make(j + 1)
			z[n:].clear()
		} else {
			z = z.make(n)
		}
		copy(z, x)
		z[j] |= m
		// no need to normalize
		return z
	}
	panic("set bit is not 0 or 1")
}

// bit returns the value of the i'th bit, with lsb == bit 0.
func (x nat) bit(i uint) uint {
	j := i / _W
	if j >= uint(len(x)) {
		return 0
	}
	// 0 <= j < len(x)
	return uint(x[j] >> (i % _W) & 1)
}

// sticky returns 1 if there's a 1 bit within the
// i least significant bits, otherwise it returns 0.
func (x nat) sticky(i uint) uint {
	j := i / _W
	if j >= uint(len(x)) {
		if len(x) == 0 {
			return 0
		}
		return 1
	}
	// 0 <= j < len(x)
	for _, x := range x[:j] {
		if x != 0 {
			return 1
		}
	}
	if x[j]<<(_W-i%_W) != 0 {
		return 1
	}
	return 0
}

func (z nat) and(x, y nat) nat {
	m := len(x)
	n := len(y)
	if m > n {
		m = n
	}
	// m <= n

	z = z.make(m)
	for i := 0; i < m; i++ {
		z[i] = x[i] & y[i]
	}

	return z.norm()
}

func (z nat) andNot(x, y nat) nat {
	m := len(x)
	n := len(y)
	if n > m {
		n = m
	}
	// m >= n

	z = z.make(m)
	for i := 0; i < n; i++ {
		z[i] = x[i] &^ y[i]
	}
	copy(z[n:m], x[n:m])

	return z.norm()
}

func (z nat) or(x, y nat) nat {
	m := len(x)
	n := len(y)
	s := x
	if m < n {
		n, m = m, n
		s = y
	}
	// m >= n

	z = z.make(m)
	for i := 0; i < n; i++ {
		z[i] = x[i] | y[i]
	}
	copy(z[n:m], s[n:m])

	return z.norm()
}

func (z nat) xor(x, y nat) nat {
	m := len(x)
	n := len(y)
	s := x
	if m < n {
		n, m = m, n
		s = y
	}
	// m >= n

	z = z.make(m)
	for i := 0; i < n; i++ {
		z[i] = x[i] ^ y[i]
	}
	copy(z[n:m], s[n:m])

	return z.norm()
}

// greaterThan reports whether (x1<<_W + x2) > (y1<<_W + y2)
func greaterThan(x1, x2, y1, y2 Word) bool {
	return x1 > y1 || x1 == y1 && x2 > y2
}

// modW returns x % d.
func (x nat) modW(d Word) (r Word) {
	// TODO(agl): we don't actually need to store the q value.
	var q nat
	q = q.make(len(x))
	return divWVW(q, 0, x, d)
}

// random creates a random integer in [0..limit), using the space in z if
// possible. n is the bit length of limit.
func (z nat) random(rand *rand.Rand, limit nat, n int) nat {
	if alias(z, limit) {
		z = nil // z is an alias for limit - cannot reuse
	}
	z = z.make(len(limit))

	bitLengthOfMSW := uint(n % _W)
	if bitLengthOfMSW == 0 {
		bitLengthOfMSW = _W
	}
	mask := Word((1 << bitLengthOfMSW) - 1)

	for {
		switch _W {
		case 32:
			for i := range z {
				z[i] = Word(rand.Uint32())
			}
		case 64:
			for i := range z {
				z[i] = Word(rand.Uint32()) | Word(rand.Uint32())<<32
			}
		default:
			panic("unknown word size")
		}
		z[len(limit)-1] &= mask
		if z.cmp(limit) < 0 {
			break
		}
	}

	return z.norm()
}

// If m != 0 (i.e., len(m) != 0), expNN sets z to x**y mod m;
// otherwise it sets z to x**y. The result is the value of z.
func (z nat) cexpNN(x, y, m nat, zcap int) nat {
	if alias(z, x) || alias(z, y) {
		// We cannot allow in-place modification of x or y.
		z = nil
	}

	// x**y mod 1 == 0
	if len(m) == 1 && m[0] == 1 {
		return z.csetWord(0, zcap)
	}
	// m == 0 || m > 1

	// x**0 == 1
	if len(y) == 0 {
		return z.csetWord(1, zcap)
	}
	// y > 0

	// x**1 mod m == x mod m
	if len(y) == 1 && y[0] == 1 && len(m) != 0 {
		_, z = z.div(z, x, m)
		return z
	}
	// y > 1

	if len(m) != 0 {
		// We likely end up being as long as the modulus.
		z = z.cmake(len(m), zcap)
	}
	z = z.cset(x, zcap)

	// If the base is non-trivial and the exponent is large, we use
	// 4-bit, windowed exponentiation. This involves precomputing 14 values
	// (x^2...x^15) but then reduces the number of multiply-reduces by a
	// third. Even for a 32-bit exponent, this reduces the number of
	// operations. Uses Montgomery method for odd moduli.
	if x.cmp(natOne) > 0 && len(y) > 1 && len(m) > 0 {
		if m[0]&1 == 1 {
			return z.expNNMontgomery(x, y, m, zcap)
		}
		return z.expNNWindowed(x, y, m, zcap)
	}

	v := y[len(y)-1] // v > 0 because y is normalized and y > 0
	shift := nlz(v) + 1
	v <<= shift
	var q nat

	const mask = 1 << (_W - 1)

	// We walk through the bits of the exponent one by one. Each time we
	// see a bit, we square, thus doubling the power. If the bit is a one,
	// we also multiply by x, thus adding one to the power.

	w := _W - int(shift)
	// zz and r are used to avoid allocating in mul and div as
	// otherwise the arguments would alias.
	var zz, r nat
	for j := 0; j < w; j++ {
		zz = zz.mul(z, z)
		zz, z = z, zz

		if v&mask != 0 {
			zz = zz.mul(z, x)
			zz, z = z, zz
		}

		if len(m) != 0 {
			zz, r = zz.div(r, z, m)
			zz, r, q, z = q, z, zz, r
		}

		v <<= 1
	}

	for i := len(y) - 2; i >= 0; i-- {
		v = y[i]

		for j := 0; j < _W; j++ {
			zz = zz.mul(z, z)
			zz, z = z, zz

			if v&mask != 0 {
				zz = zz.mul(z, x)
				zz, z = z, zz
			}

			if len(m) != 0 {
				zz, r = zz.div(r, z, m)
				zz, r, q, z = q, z, zz, r
			}

			v <<= 1
		}
	}

	return z.cnorm(zcap)
}

func (z nat) expNN(x, y, m nat) nat {
	return z.cexpNN(x, y, m, 0)
}

// expNNWindowed calculates x**y mod m using a fixed, 4-bit window.
func (z nat) expNNWindowed(x, y, m nat, zcap int) nat {
	// zz and r are used to avoid allocating in mul and div as otherwise
	// the arguments would alias.
	var zz, r nat

	const n = 4
	// powers[i] contains x^i.
	var powers [1 << n]nat
	powers[0] = natOne
	powers[1] = x
	for i := 2; i < 1<<n; i += 2 {
		p2, p, p1 := &powers[i/2], &powers[i], &powers[i+1]
		*p = p.mul(*p2, *p2)
		zz, r = zz.div(r, *p, m)
		*p, r = r, *p
		*p1 = p1.mul(*p, x)
		zz, r = zz.div(r, *p1, m)
		*p1, r = r, *p1
	}

	z = z.csetWord(1, zcap)

	for i := len(y) - 1; i >= 0; i-- {
		yi := y[i]
		for j := 0; j < _W; j += n {
			if i != len(y)-1 || j != 0 {
				// Unrolled loop for significant performance
				// gain. Use go test -bench=".*" in crypto/rsa
				// to check performance before making changes.
				zz = zz.mul(z, z)
				zz, z = z, zz
				zz, r = zz.div(r, z, m)
				z, r = r, z

				zz = zz.mul(z, z)
				zz, z = z, zz
				zz, r = zz.div(r, z, m)
				z, r = r, z

				zz = zz.mul(z, z)
				zz, z = z, zz
				zz, r = zz.div(r, z, m)
				z, r = r, z

				zz = zz.mul(z, z)
				zz, z = z, zz
				zz, r = zz.div(r, z, m)
				z, r = r, z
			}

			zz = zz.mul(z, powers[yi>>(_W-n)])
			zz, z = z, zz
			zz, r = zz.div(r, z, m)
			z, r = r, z

			yi <<= n
		}
	}

	return z.cnorm(zcap)
}

// expNNMontgomery calculates x**y mod m using a fixed, 4-bit window.
// Uses Montgomery representation.
func (z nat) expNNMontgomery(x, y, m nat, zcap int) nat {
	numWords := len(m)

	// We want the lengths of x and m to be equal.
	// It is OK if x >= m as long as len(x) == len(m).
	if len(x) > numWords {
		_, x = nat(nil).div(nil, x, m)
		// Note: now len(x) <= numWords, not guaranteed ==.
	}
	if len(x) < numWords {
		rr := make(nat, numWords)
		copy(rr, x)
		x = rr
	}

	// Ideally the precomputations would be performed outside, and reused
	// k0 = -m**-1 mod 2**_W. Algorithm from: Dumas, J.G. "On Newton–Raphson
	// Iteration for Multiplicative Inverses Modulo Prime Powers".
	k0 := 2 - m[0]
	t := m[0] - 1
	for i := 1; i < _W; i <<= 1 {
		t *= t
		k0 *= (t + 1)
	}
	k0 = -k0

	// RR = 2**(2*_W*len(m)) mod m
	RR := nat(nil).csetWord(1, zcap)
	zz := nat(nil).shl(RR, uint(2*numWords*_W))
	_, RR = RR.div(RR, zz, m)
	if len(RR) < numWords {
		zz = zz.cmake(numWords, zcap)
		copy(zz, RR)
		RR = zz
	}
	// one = 1, with equal length to that of m
	one := make(nat, numWords)
	one[0] = 1

	// for constant-time operation we'll need a temporary to select from
	zt := nat(nil)
	if zcap > 0 {
		zt = make(nat, numWords)
	}

	const n = 4
	// powers[i] contains x^i
	var powers [1 << n]nat
	powers[0] = powers[0].montgomery(one, RR, m, k0, numWords, zt, zcap)
	powers[1] = powers[1].montgomery(x, RR, m, k0, numWords, zt, zcap)
	for i := 2; i < 1<<n; i++ {
		powers[i] = powers[i].montgomery(powers[i-1], powers[1], m, k0, numWords, zt, zcap)
	}

	// initialize z = 1 (Montgomery 1)
	z = z.cmake(numWords, zcap)
	copy(z, powers[0])

	zz = zz.cmake(numWords, zcap)

	// same windowed exponent, but with Montgomery multiplications
	for i := len(y) - 1; i >= 0; i-- {
		yi := y[i]
		for j := 0; j < _W; j += n {
			if i != len(y)-1 || j != 0 {
				zz = zz.montgomery(z, z, m, k0, numWords, zt, zcap)
				z = z.montgomery(zz, zz, m, k0, numWords, zt, zcap)
				zz = zz.montgomery(z, z, m, k0, numWords, zt, zcap)
				z = z.montgomery(zz, zz, m, k0, numWords, zt, zcap)
			}
			zz = zz.montgomery(z, powers[yi>>(_W-n)], m, k0, numWords, zt, zcap)
			z, zz = zz, z
			yi <<= n
		}
	}
	// convert to regular number
	zz = zz.montgomery(z, one, m, k0, numWords, zt, zcap)

	// One last reduction, just in case.
	// See golang.org/issue/13907.
	if zz.cmp(m) >= 0 {
		// Common case is m has high bit set; in that case,
		// since zz is the same length as m, there can be just
		// one multiple of m to remove. Just subtract.
		// We think that the subtract should be sufficient in general,
		// so do that unconditionally, but double-check,
		// in case our beliefs are wrong.
		// The div is not expected to be reached.
		zz = zz.sub(zz, m)
		if zz.cmp(m) >= 0 {
			_, zz = nat(nil).div(nil, zz, m)
		}
	}

	return zz.cnorm(zcap)
}

// bytes writes the value of z into buf using big-endian encoding.
// len(buf) must be >= len(z)*_S. The value of z is encoded in the
// slice buf[i:]. The number i of unused bytes at the beginning of
// buf is returned as result.
func (z nat) bytes(buf []byte) (i int) {
	i = len(buf)
	for _, d := range z {
		for j := 0; j < _S; j++ {
			i--
			buf[i] = byte(d)
			d >>= 8
		}
	}

	for i < len(buf) && buf[i] == 0 {
		i++
	}

	return
}

// setBytes interprets buf as the bytes of a big-endian unsigned
// integer, sets z to that value, and returns z.
func (z nat) csetBytes(buf []byte, zcap int) nat {
	z = z.cmake((len(buf)+_S-1)/_S, zcap)

	k := 0
	s := uint(0)
	var d Word
	for i := len(buf); i > 0; i-- {
		d |= Word(buf[i-1]) << s
		if s += 8; s == _S*8 {
			z[k] = d
			k++
			s = 0
			d = 0
		}
	}
	if k < len(z) {
		z[k] = d
	}

	return z.cnorm(zcap)
}

func (z nat) setBytes(buf []byte) nat {
	return z.csetBytes(buf, 0)
}

// sqrt sets z = ⌊√x⌋
func (z nat) sqrt(x nat) nat {
	if x.cmp(natOne) <= 0 {
		return z.set(x)
	}
	if alias(z, x) {
		z = nil
	}

	// Start with value known to be too large and repeat "z = ⌊(z + ⌊x/z⌋)/2⌋" until it stops getting smaller.
	// See Brent and Zimmermann, Modern Computer Arithmetic, Algorithm 1.13 (SqrtInt).
	// https://members.loria.fr/PZimmermann/mca/pub226.html
	// If x is one less than a perfect square, the sequence oscillates between the correct z and z+1;
	// otherwise it converges to the correct z and stays there.
	var z1, z2 nat
	z1 = z
	z1 = z1.setUint64(1)
	z1 = z1.shl(z1, uint(x.bitLen()/2+1)) // must be ≥ √x
	for n := 0; ; n++ {
		z2, _ = z2.div(nil, x, z1)
		z2 = z2.add(z2, z1)
		z2 = z2.shr(z2, 1)
		if z2.cmp(z1) >= 0 {
			// z1 is answer.
			// Figure out whether z1 or z2 is currently aliased to z by looking at loop count.
			if n&1 == 0 {
				return z1
			}
			return z.set(z1)
		}
		z1, z2 = z2, z1
	}
}
