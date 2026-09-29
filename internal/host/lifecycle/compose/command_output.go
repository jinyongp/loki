package compose

// commandOutput retains a bounded success prefix and failure tail. Docker pull
// progress can be much larger than the useful error emitted at the end.
type commandOutput struct {
	head []byte
	tail []byte
}

func (b *commandOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	if room := maxCommandOutput - len(b.head); room > 0 {
		b.head = append(b.head, raw[:min(room, n)]...)
	}
	if n >= maxCommandOutput {
		b.tail = append(b.tail[:0], raw[n-maxCommandOutput:]...)
		return n, nil
	}
	if overflow := len(b.tail) + n - maxCommandOutput; overflow > 0 {
		copy(b.tail, b.tail[overflow:])
		b.tail = b.tail[:len(b.tail)-overflow]
	}
	b.tail = append(b.tail, raw...)
	return n, nil
}
