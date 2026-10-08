//go:build arm64 && goexperiment.simd && !purego

package stage1

import "simd/archsimd"

// classify tables, adapted from the C++ arm64 json_character_block::classify.
var (
	// opTable[(c+3)>>4] == c exactly when c is one of ,:[]{}. Entry 0 is 0x80
	// (C++ uses 0xff) so that no byte of its group (0..12, 253..255) matches.
	opTable = [16]uint8{0x80, 0, ',', ':', 0, '[', ']', '{', '}'}
	// wsTable[c] == c exactly for \t \n \r; space is compared separately.
	wsTable = [16]uint8{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, '\t', '\n', 0xff, 0xff, '\r', 0xff, 0xff}
	// interleave moves bytes 0..7 to even and 8..15 to odd positions. With
	// bitWeights, a 16-bit pairwise-add ladder then yields masks in natural
	// bit order (NEON has no movemask and archsimd has no byte-wise ADDP).
	interleave = [16]uint8{0, 8, 1, 9, 2, 10, 3, 11, 4, 12, 5, 13, 6, 14, 7, 15}
	bitWeights = [16]uint8{1, 1, 2, 2, 4, 4, 8, 8, 16, 16, 32, 32, 64, 64, 128, 128}
)

func chunk(b *[64]byte, k int) archsimd.Uint8x16 {
	return archsimd.LoadUint8x16Array((*[16]uint8)(b[16*k : 16*k+16]))
}

func classify(b *[64]byte) masks {
	var (
		perm  = archsimd.LoadUint8x16Array(&interleave)
		w     = archsimd.LoadUint8x16Array(&bitWeights)
		ops   = archsimd.LoadUint8x16Array(&opTable)
		wss   = archsimd.LoadUint8x16Array(&wsTable)
		quote = archsimd.BroadcastUint8x16('"')
		bsl   = archsimd.BroadcastUint8x16('\\')
		space = archsimd.BroadcastUint8x16(' ')
		three = archsimd.BroadcastUint8x16(3)
		lt20  = archsimd.BroadcastUint8x16(0x20)

		q, bs, ws, op, ctrl [4]archsimd.Uint8x16
	)
	for k := range 4 {
		c := chunk(b, k).LookupOrZero(perm) // c[i] = chunk[interleave[i]]
		q[k] = w.Masked(c.Equal(quote))
		bs[k] = w.Masked(c.Equal(bsl))
		ws[k] = w.Masked(wss.LookupOrZero(c).Equal(c).Or(c.Equal(space)))
		op[k] = w.Masked(ops.LookupOrZero(c.Add(three).ShiftAllRight(4)).Equal(c))
		ctrl[k] = w.Masked(c.Less(lt20))
	}
	lc := ladder(ctrl)
	qb, wo, cc := pack(q, bs), pack(ws, op), lc.ConcatAddPairs(lc).ReshapeToUint64s()
	return masks{
		quote: qb.GetElem(0), backslash: qb.GetElem(1),
		ws: wo.GetElem(0), op: wo.GetElem(1),
		ctrl: cc.GetElem(0),
	}
}

// pack reduces two sets of four bit-weighted chunks to two 64-bit masks:
// lane 0 holds a's mask and lane 1 holds b's.
func pack(a, b [4]archsimd.Uint8x16) archsimd.Uint64x2 {
	return ladder(a).ConcatAddPairs(ladder(b)).ReshapeToUint64s()
}

// ladder returns [a0, a0, a1, a1, a2, a2, a3, a3] partial sums: after one more
// ConcatAddPairs each 16-bit lane is the full 16-bit mask of one chunk.
func ladder(a [4]archsimd.Uint8x16) archsimd.Uint16x8 {
	t0 := a[0].ReshapeToUint16s().ConcatAddPairs(a[1].ReshapeToUint16s())
	t1 := a[2].ReshapeToUint16s().ConcatAddPairs(a[3].ReshapeToUint16s())
	return t0.ConcatAddPairs(t1)
}

// UTF-8 lookup tables (src/generic/stage1/utf8_lookup4_algorithm.h).
const (
	tooShort     = 1 << 0
	tooLong      = 1 << 1
	overlong3    = 1 << 2
	tooLarge     = 1 << 3
	surrogate    = 1 << 4
	overlong2    = 1 << 5
	tooLarge1000 = 1 << 6
	overlong4    = 1 << 6
	twoConts     = 1 << 7
	carry        = tooShort | tooLong | twoConts
)

var (
	byte1High = [16]uint8{
		tooLong, tooLong, tooLong, tooLong, tooLong, tooLong, tooLong, tooLong,
		twoConts, twoConts, twoConts, twoConts,
		tooShort | overlong2,
		tooShort,
		tooShort | overlong3 | surrogate,
		tooShort | tooLarge | tooLarge1000 | overlong4,
	}
	byte1Low = [16]uint8{
		carry | overlong3 | overlong2 | overlong4,
		carry | overlong2,
		carry,
		carry,
		carry | tooLarge,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000 | surrogate,
		carry | tooLarge | tooLarge1000,
		carry | tooLarge | tooLarge1000,
	}
	byte2High = [16]uint8{
		tooShort, tooShort, tooShort, tooShort, tooShort, tooShort, tooShort, tooShort,
		tooLong | overlong2 | twoConts | overlong3 | tooLarge1000 | overlong4,
		tooLong | overlong2 | twoConts | overlong3 | tooLarge,
		tooLong | overlong2 | twoConts | surrogate | tooLarge,
		tooLong | overlong2 | twoConts | surrogate | tooLarge,
		tooShort, tooShort, tooShort, tooShort,
	}
	// A block whose last 3 bytes exceed these values ends inside a character.
	maxComplete = [16]uint8{
		255, 255, 255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 0xf0 - 1, 0xe0 - 1, 0xc0 - 1,
	}
)

// utf8Checker is the lookup4 validator; it runs on every block.
type utf8Checker struct {
	err, prev, prevIncomplete archsimd.Uint8x16
}

func (u *utf8Checker) next(b *[64]byte) {
	in0, in1, in2, in3 := chunk(b, 0), chunk(b, 1), chunk(b, 2), chunk(b, 3)
	or := in0.Or(in1).Or(in2).Or(in3).ReshapeToUint64s()
	if (or.GetElem(0)|or.GetElem(1))&0x8080808080808080 == 0 {
		// An ASCII block cannot complete a character left open by the previous one.
		u.err = u.err.Or(u.prevIncomplete)
		return
	}
	u.check(in0, u.prev)
	u.check(in1, in0)
	u.check(in2, in1)
	u.check(in3, in2)
	u.prevIncomplete = in3.SubSaturated(archsimd.LoadUint8x16Array(&maxComplete))
	u.prev = in3
}

func (u *utf8Checker) check(in, prev archsimd.Uint8x16) {
	low4 := archsimd.BroadcastUint8x16(0x0f)
	// x.ConcatShiftBytesRight(y, n) is bytes n.. of y:x (y is the low half), so
	// in.ConcatShiftBytesRight(prev, 16-k)[i] is the byte k positions before in[i].
	prev1 := in.ConcatShiftBytesRight(prev, 15)
	sc := archsimd.LoadUint8x16Array(&byte1High).LookupOrZero(prev1.ShiftAllRight(4)).
		And(archsimd.LoadUint8x16Array(&byte1Low).LookupOrZero(prev1.And(low4))).
		And(archsimd.LoadUint8x16Array(&byte2High).LookupOrZero(in.ShiftAllRight(4)))
	prev2 := in.ConcatShiftBytesRight(prev, 14)
	prev3 := in.ConcatShiftBytesRight(prev, 13)
	must23 := prev2.SubSaturated(archsimd.BroadcastUint8x16(0xe0 - 0x80)).
		Or(prev3.SubSaturated(archsimd.BroadcastUint8x16(0xf0 - 0x80)))
	u.err = u.err.Or(must23.And(archsimd.BroadcastUint8x16(0x80)).Xor(sc))
}

// validWindow reports whether no invalid UTF-8 has been seen; at the end of
// the input it also rejects a character left incomplete.
func (u *utf8Checker) validWindow(buf []byte, _, end int) bool {
	e := u.err
	if end == len(buf) {
		e = e.Or(u.prevIncomplete)
	}
	r := e.ReshapeToUint64s()
	return r.GetElem(0)|r.GetElem(1) == 0
}
