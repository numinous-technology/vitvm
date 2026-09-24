package main

// parseFlags is a tiny parser: --key value and --key=value become options,
// everything else is positional (in rest). Enough for vit's few flags, and no
// dependency.
type flagset struct {
	opts map[string]string
	rest []string
}

func parseFlags(args []string) *flagset {
	f := &flagset{opts: map[string]string{}}
	i := 0
	for i < len(args) {
		a := args[i]
		if len(a) > 2 && a[:2] == "--" {
			key := a[2:]
			if eq := indexByte(key, '='); eq >= 0 {
				f.opts[key[:eq]] = key[eq+1:]
				i++
				continue
			}
			if i+1 < len(args) {
				f.opts[key] = args[i+1]
				i += 2
				continue
			}
			f.opts[key] = ""
			i++
			continue
		}
		f.rest = append(f.rest, a)
		i++
	}
	return f
}

func (f *flagset) str(key string) string { return f.opts[key] }

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
