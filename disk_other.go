//go:build !(linux || darwin || freebsd)

package main

// diskUsage não é suportado nesta plataforma; a interface mostra apenas o
// espaço ocupado pela galeria.
func diskUsage(path string) (total, free int64, ok bool) {
	return 0, 0, false
}
