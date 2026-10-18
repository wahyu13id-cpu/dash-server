// Package sysinfo samples host resource usage (CPU, memory, disks, network and
// processes) and exposes immutable snapshots for the dashboard. It reads the
// Linux /proc and /sys interfaces directly; no shell commands or third-party
// dependencies are involved.
package sysinfo

import "time"

// Snapshot is a point-in-time view of host resource usage. All byte counts are
// in bytes and all percentages are 0-100.
type Snapshot struct {
	CollectedAt time.Time `json:"collectedAt"`
	Host        Host      `json:"host"`
	CPU         CPU       `json:"cpu"`
	Memory      Memory    `json:"memory"`
	Load        Load      `json:"load"`
	Disks       Disks     `json:"disks"`
	Network     Network   `json:"network"`
	Processes   Processes `json:"processes"`
}

// Host describes the machine itself.
type Host struct {
	Hostname  string `json:"hostname"`
	Kernel    string `json:"kernel"`
	Arch      string `json:"arch"`
	UptimeSec int64  `json:"uptimeSec"`
}

// CPU holds aggregate and per-core utilisation plus a short history for
// sparklines. History is oldest-first.
type CPU struct {
	UsagePercent float64   `json:"usagePercent"`
	Cores        int       `json:"cores"`
	Model        string    `json:"model"`
	PerCore      []float64 `json:"perCore"`
	History      []float64 `json:"history"`
}

// Memory is the memory and swap breakdown.
type Memory struct {
	TotalBytes      uint64  `json:"totalBytes"`
	UsedBytes       uint64  `json:"usedBytes"`
	AvailableBytes  uint64  `json:"availableBytes"`
	FreeBytes       uint64  `json:"freeBytes"`
	CachedBytes     uint64  `json:"cachedBytes"`
	BuffersBytes    uint64  `json:"buffersBytes"`
	UsedPercent     float64 `json:"usedPercent"`
	SwapTotalBytes  uint64  `json:"swapTotalBytes"`
	SwapUsedBytes   uint64  `json:"swapUsedBytes"`
	SwapUsedPercent float64 `json:"swapUsedPercent"`
}

// Load is the Unix load average and process counts.
type Load struct {
	Load1   float64 `json:"load1"`
	Load5   float64 `json:"load5"`
	Load15  float64 `json:"load15"`
	Running int     `json:"running"`
	Total   int     `json:"total"`
}

// Disks aggregates mounted filesystems.
type Disks struct {
	Filesystems []Disk  `json:"filesystems"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// Disk is a single mounted filesystem.
type Disk struct {
	Device      string  `json:"device"`
	Mount       string  `json:"mount"`
	FSType      string  `json:"fsType"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// Network aggregates throughput and per-interface counters. Loopback traffic is
// reported per interface but excluded from the aggregate totals.
type Network struct {
	RxBytesPerSec float64    `json:"rxBytesPerSec"`
	TxBytesPerSec float64    `json:"txBytesPerSec"`
	RxTotalBytes  uint64     `json:"rxTotalBytes"`
	TxTotalBytes  uint64     `json:"txTotalBytes"`
	Interfaces    []NetIface `json:"interfaces"`
}

// NetIface is a single network interface's counters and rates.
type NetIface struct {
	Name          string  `json:"name"`
	RxBytes       uint64  `json:"rxBytes"`
	TxBytes       uint64  `json:"txBytes"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
}

// Processes holds the top resource consumers and the total process count.
type Processes struct {
	Total    int       `json:"total"`
	ByCPU    []Process `json:"byCpu"`
	ByMemory []Process `json:"byMemory"`
}

// Process is a single process entry. CPUPercent follows the top(1) convention
// and may exceed 100 on multi-core systems.
type Process struct {
	PID        int     `json:"pid"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	MemPercent float64 `json:"memPercent"`
	RSSBytes   uint64  `json:"rssBytes"`
	Threads    int     `json:"threads"`
	Command    string  `json:"command"`
}
