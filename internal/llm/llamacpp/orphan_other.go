//go:build !linux

package llamacpp

import "log/slog"

/*
ReapOrphanedServers is a no-op on platforms without /proc. On Darwin/Windows the
in-process Close/Reclaim path still supersedes a prior runtime; cross-process orphan
reaping would need a ps/toolhelp scan, which is not implemented here.
*/
func ReapOrphanedServers(_ *slog.Logger) {}
