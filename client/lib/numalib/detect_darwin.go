// Copyright IBM Corp. 2015, 2025
// SPDX-License-Identifier: BUSL-1.1

//go:build darwin

package numalib

import (
	"github.com/hashicorp/nomad/client/lib/idset"
	"github.com/hashicorp/nomad/client/lib/numalib/hw"
	"github.com/shoenig/go-m1cpu"
	"golang.org/x/sys/unix"
)

// PlatformScanners returns the set of SystemScanner for macOS.
func PlatformScanners(_ bool) []SystemScanner {
	return []SystemScanner{
		new(MacOS),
	}
}

const (
	nodeID   = hw.NodeID(0)
	socketID = hw.SocketID(0)
	maxSpeed = hw.KHz(0)

	// fallbackCoreSpeed is used when neither go-m1cpu nor sysctl can report a
	// CPU frequency. This happens on virtualized Apple Silicon (e.g. the M4
	// CI machines, brand string "Apple M4 (Virtual)"), where the IORegistry
	// voltage-states used by go-m1cpu are absent and hw.cpufrequency is not
	// published. Without a non-zero base speed the node's cpu.totalcompute is
	// zero and no task can ever be scheduled. 2.4 GHz is a deliberately
	// conservative stand-in so the scheduler sees real capacity.
	fallbackCoreSpeed = hw.MHz(2400)
)

// MacOS implements SystemScanner for macOS systems (both arm64 and x86).
type MacOS struct{}

func (m *MacOS) ScanSystem(top *Topology) {
	// all apple hardware is non-numa; just assume as much
	top.nodeIDs = idset.Empty[hw.NodeID]()
	top.nodeIDs.Insert(nodeID)

	// arch specific detection
	switch m1cpu.IsAppleSilicon() {
	case true:
		m.scanAppleSilicon(top)
	case false:
		m.scanLegacyX86(top)
	}
}

func (m *MacOS) scanAppleSilicon(top *Topology) {
	pCoreCount := m1cpu.PCoreCount()
	pCoreSpeed := hw.KHz(m1cpu.PCoreHz() / 1000)
	eCoreSpeed := hw.KHz(m1cpu.ECoreHz() / 1000)

	// On some (notably virtualized) Apple Silicon hosts such as the M4 CI
	// machines, the go-m1cpu library reports P/E core counts that do not add
	// up to the real number of CPUs exposed by the kernel. Trusting those
	// counts caused an index-out-of-range panic in this function when the
	// number of cores inserted did not match the size of the Cores slice.
	//
	// To be robust we size the slice from the authoritative sysctl core count
	// and drive the insert loop from that same number, so the slice size and
	// the number of inserts can never diverge. Cores up to the reported
	// performance-core count are treated as performance cores; any remaining
	// cores are treated as efficiency cores.
	totalCores, err := unix.SysctlUint32("hw.ncpu")
	if err != nil || totalCores == 0 {
		totalCores = uint32(pCoreCount + m1cpu.ECoreCount())
	}

	// Reconcile core speeds. On virtualized Apple Silicon both go-m1cpu and
	// sysctl fail to report a frequency, leaving these at zero, which makes
	// cpu.totalcompute zero. Fall back from one core type to the other, then
	// to a fixed stand-in, so cores always carry a non-zero base speed.
	if pCoreSpeed == 0 {
		pCoreSpeed = eCoreSpeed
	}
	if eCoreSpeed == 0 {
		eCoreSpeed = pCoreSpeed
	}
	if pCoreSpeed == 0 {
		pCoreSpeed = hw.KHz(fallbackCoreSpeed.KHz())
	}
	if eCoreSpeed == 0 {
		eCoreSpeed = pCoreSpeed
	}

	top.Cores = make([]Core, totalCores)
	for i := uint32(0); i < totalCores; i++ {
		grade := Performance
		speed := pCoreSpeed
		if int(i) >= pCoreCount {
			grade = Efficiency
			speed = eCoreSpeed
		}
		top.insert(nodeID, socketID, hw.CoreID(i), grade, maxSpeed, speed)
	}
}

func (m *MacOS) scanLegacyX86(top *Topology) {
	coreCount, _ := unix.SysctlUint32("hw.ncpu")
	hz, _ := unix.SysctlUint64("hw.cpufrequency")
	coreSpeed := hw.KHz(hz / 1_000)
	top.Cores = make([]Core, coreCount)
	for i := 0; i < int(coreCount); i++ {
		top.insert(nodeID, socketID, hw.CoreID(i), Performance, maxSpeed, coreSpeed)
	}
}
