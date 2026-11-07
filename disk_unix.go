//go:build linux || darwin || freebsd

package main

import "syscall"

// diskUsage retorna o tamanho total e o espaço livre (disponível para usuários
// sem privilégio) do sistema de arquivos onde path está.
func diskUsage(path string) (total, free int64, ok bool) {
	var st syscall.Statfs_t

	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}

	blockSize := int64(st.Bsize)

	return int64(st.Blocks) * blockSize, int64(st.Bavail) * blockSize, true
}
