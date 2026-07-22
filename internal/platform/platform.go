// Package platform owns the OS-specific runtime boundary (FEAT-20260722-002,
// platform-runtime contract). Five capability axes are split behind explicit
// build constraints — exclusive objective lock, process-tree lifecycle (in
// package adapter), private create/verify, atomic replace + durability, and
// parent interrupt/termination classification. Every implementation keeps the
// shared fail-closed semantics: an unsupported or unverifiable capability
// returns an explicit diagnostic error and never degrades silently.
//
// Supported platforms are darwin, linux (POSIX lane) and windows. Other
// GOOS values fail to compile rather than accidentally selecting the POSIX
// implementation.
package platform
