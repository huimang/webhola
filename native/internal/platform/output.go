package platform

type TailWriter struct{ Buf []byte }

func (w *TailWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.Buf = append(w.Buf, p...)
	if len(w.Buf) > 8192 {
		w.Buf = w.Buf[len(w.Buf)-8192:]
	}
	return n, nil
}
