// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! Write-Ahead Log (WAL) engine for nvstor distributed storage.
//!
//! Provides durable, ordered logging of all storage mutations before they are applied.
//! Supports segment-based storage with rotation, IEEE CRC-32 integrity verification, and
//! crash-recovery replay from any sequence number.
//!
//! # Segment Format
//!
//! Each segment file starts with a 64-byte header, followed by variable-length entries,
//! and optionally ends with a 16-byte footer on clean close.
//!
//! # Invariants
//!
//! - Sequence numbers are monotonically increasing and gapless (starting from 1).
//! - IEEE CRC-32 of each entry payload is verified on read; mismatch stops replay.
//! - Replay stops at corruption boundary (partial write = end of log).
//! - Segment files are named `wal-{segment_id:016x}.log` and read in order.

use std::fs::{self, File, OpenOptions};
use std::io::{self, BufReader, BufWriter, Read, Write};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/// Magic bytes: "BED_WAL\0"
const SEGMENT_MAGIC: u64 = 0x4245445F57414C00;

/// Current segment format version.
const SEGMENT_VERSION: u32 = 1;

/// Segment header size in bytes.
const SEGMENT_HEADER_SIZE: u64 = 64;

/// Entry header size: 4 (len) + 8 (seq) + 4 (crc) = 16 bytes.
const ENTRY_HEADER_SIZE: usize = 16;

/// Segment footer size: 4 (count) + 8 (last_seq) + 4 (crc) = 16 bytes.
const FOOTER_SIZE: usize = 16;

/// Default maximum segment size: 1 GB.
const DEFAULT_MAX_SEGMENT_BYTES: u64 = 1 << 30;

// ---------------------------------------------------------------------------
// WalOperation
// ---------------------------------------------------------------------------

/// A single logical mutation to be durably recorded before applying.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum WalOperation {
    // Volume operations
    CreateVolume {
        volume_id: [u8; 16],
        name: String,
        size: u64,
        replication: u8,
    },
    DeleteVolume {
        volume_id: [u8; 16],
    },

    // File / inode operations
    CreateFile {
        volume_id: [u8; 16],
        path: String,
        inode: u64,
    },
    WriteChunks {
        inode: u64,
        offset: u64,
        chunks: Vec<[u8; 32]>,
    },
    Truncate {
        inode: u64,
        size: u64,
    },
    Delete {
        volume_id: [u8; 16],
        path: String,
    },
    Rename {
        volume_id: [u8; 16],
        from: String,
        to: String,
    },

    // Blob operations (content-addressed)
    CreateBlob {
        id: [u8; 32],
        chunks: Vec<[u8; 32]>,
    },
    DeleteBlob {
        id: [u8; 32],
    },

    // Object operations (S3)
    PutObject {
        object_id: String,
        size: u64,
        etag: String,
    },
    DeleteObject {
        object_id: String,
    },

    // Checkpoint marker
    Checkpoint {
        sequence: u64,
    },

    // No-op (for padding / testing)
    Noop,
}

// ---------------------------------------------------------------------------
// WalEntry
// ---------------------------------------------------------------------------

/// A single entry read back from the WAL (sequence + verified payload).
#[derive(Debug, Clone)]
pub struct WalEntry {
    pub sequence: u64,
    pub checksum: u32,
    pub operation: WalOperation,
}

// ---------------------------------------------------------------------------
// WalConfig
// ---------------------------------------------------------------------------

/// Configuration for the WAL writer.
#[derive(Debug, Clone)]
pub struct WalConfig {
    /// Maximum segment size in bytes before rotation (default: 1 GB).
    pub max_segment_bytes: u64,
    /// Whether to fsync after every append (default: false).
    pub sync_on_append: bool,
}

impl Default for WalConfig {
    fn default() -> Self {
        Self {
            max_segment_bytes: DEFAULT_MAX_SEGMENT_BYTES,
            sync_on_append: false,
        }
    }
}

// ---------------------------------------------------------------------------
// WalError (local, avoids coupling to error.rs being built concurrently)
// ---------------------------------------------------------------------------

/// WAL-specific errors.
#[derive(Debug)]
pub enum WalError {
    Io(io::Error),
    Corrupt(String),
    Bincode(bincode::Error),
    InvalidMagic,
    UnsupportedVersion(u32),
}

impl std::fmt::Display for WalError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            WalError::Io(e) => write!(f, "WAL I/O error: {e}"),
            WalError::Corrupt(msg) => write!(f, "WAL corruption: {msg}"),
            WalError::Bincode(e) => write!(f, "WAL serialization error: {e}"),
            WalError::InvalidMagic => write!(f, "WAL invalid segment magic"),
            WalError::UnsupportedVersion(v) => {
                write!(f, "WAL unsupported segment version: {v}")
            }
        }
    }
}

impl std::error::Error for WalError {}

impl From<io::Error> for WalError {
    fn from(e: io::Error) -> Self {
        WalError::Io(e)
    }
}

impl From<bincode::Error> for WalError {
    fn from(e: bincode::Error) -> Self {
        WalError::Bincode(e)
    }
}

/// Convenience alias scoped to WAL operations.
pub type WalResult<T> = std::result::Result<T, WalError>;

// ---------------------------------------------------------------------------
// Segment naming helpers
// ---------------------------------------------------------------------------

fn segment_filename(segment_id: u64) -> String {
    format!("wal-{segment_id:016x}.log")
}

fn parse_segment_id(name: &str) -> Option<u64> {
    let name = name.strip_prefix("wal-")?.strip_suffix(".log")?;
    u64::from_str_radix(name, 16).ok()
}

/// Return sorted list of (segment_id, path) in the WAL directory.
fn list_segments(dir: &Path) -> WalResult<Vec<(u64, PathBuf)>> {
    let mut segments = Vec::new();
    if !dir.exists() {
        return Ok(segments);
    }
    for entry in fs::read_dir(dir)? {
        let entry = entry?;
        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        if let Some(id) = parse_segment_id(&name_str) {
            segments.push((id, entry.path()));
        }
    }
    segments.sort_by_key(|(id, _)| *id);
    Ok(segments)
}

// ---------------------------------------------------------------------------
// Segment header / footer I/O
// ---------------------------------------------------------------------------

fn write_segment_header(w: &mut impl Write, segment_id: u64) -> WalResult<()> {
    let mut buf = [0u8; SEGMENT_HEADER_SIZE as usize];
    buf[0..8].copy_from_slice(&SEGMENT_MAGIC.to_le_bytes());
    buf[8..12].copy_from_slice(&SEGMENT_VERSION.to_le_bytes());
    buf[12..20].copy_from_slice(&segment_id.to_le_bytes());
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;
    buf[20..28].copy_from_slice(&now.to_le_bytes());
    // Bytes 28-63 reserved (already zeroed).
    w.write_all(&buf)?;
    Ok(())
}

fn read_segment_header(r: &mut impl Read) -> WalResult<u64> {
    let mut buf = [0u8; SEGMENT_HEADER_SIZE as usize];
    r.read_exact(&mut buf)?;

    let magic = u64::from_le_bytes(buf[0..8].try_into().expect("fixed-size slice"));
    if magic != SEGMENT_MAGIC {
        return Err(WalError::InvalidMagic);
    }
    let version = u32::from_le_bytes(buf[8..12].try_into().expect("fixed-size slice"));
    if version != SEGMENT_VERSION {
        return Err(WalError::UnsupportedVersion(version));
    }
    let segment_id = u64::from_le_bytes(buf[12..20].try_into().expect("fixed-size slice"));
    Ok(segment_id)
}

fn write_segment_footer(w: &mut impl Write, entry_count: u32, last_seq: u64) -> WalResult<()> {
    let mut buf = [0u8; FOOTER_SIZE];
    buf[0..4].copy_from_slice(&entry_count.to_le_bytes());
    buf[4..12].copy_from_slice(&last_seq.to_le_bytes());
    let crc = crc32fast::hash(&buf[0..12]);
    buf[12..16].copy_from_slice(&crc.to_le_bytes());
    w.write_all(&buf)?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Entry I/O
// ---------------------------------------------------------------------------

fn write_entry(w: &mut impl Write, seq: u64, payload: &[u8]) -> WalResult<()> {
    let len = payload.len() as u32;
    let crc = crc32fast::hash(payload);

    let mut hdr = [0u8; ENTRY_HEADER_SIZE];
    hdr[0..4].copy_from_slice(&len.to_le_bytes());
    hdr[4..12].copy_from_slice(&seq.to_le_bytes());
    hdr[12..16].copy_from_slice(&crc.to_le_bytes());

    w.write_all(&hdr)?;
    w.write_all(payload)?;
    Ok(())
}

/// Attempt to read one entry from a reader. Returns `None` at clean EOF or partial header.
/// Returns `Err(WalError::Corrupt(_))` on CRC mismatch.
fn read_entry(r: &mut impl Read) -> WalResult<Option<WalEntry>> {
    // Read header.
    let mut hdr = [0u8; ENTRY_HEADER_SIZE];
    match read_exact_or_eof(r, &mut hdr)? {
        ReadExact::Eof | ReadExact::Partial => return Ok(None),
        ReadExact::Ok => {}
    }

    let payload_len = u32::from_le_bytes(hdr[0..4].try_into().expect("fixed-size slice")) as usize;
    let seq = u64::from_le_bytes(hdr[4..12].try_into().expect("fixed-size slice"));
    let expected_crc = u32::from_le_bytes(hdr[12..16].try_into().expect("fixed-size slice"));

    // Read payload.
    let mut payload = vec![0u8; payload_len];
    match read_exact_or_eof(r, &mut payload)? {
        ReadExact::Eof | ReadExact::Partial => return Ok(None), // truncated entry
        ReadExact::Ok => {}
    }

    // Verify CRC.
    let actual_crc = crc32fast::hash(&payload);
    if actual_crc != expected_crc {
        return Err(WalError::Corrupt(format!(
            "CRC mismatch at seq {seq}: expected {expected_crc:#010x}, got {actual_crc:#010x}"
        )));
    }

    let operation: WalOperation = bincode::deserialize(&payload)?;

    Ok(Some(WalEntry {
        sequence: seq,
        checksum: actual_crc,
        operation,
    }))
}

/// Helper that distinguishes clean EOF, partial read, and full read.
enum ReadExact {
    Ok,
    Eof,     // zero bytes read
    Partial, // some bytes but not all
}

fn read_exact_or_eof(r: &mut impl Read, buf: &mut [u8]) -> io::Result<ReadExact> {
    let mut total = 0;
    while total < buf.len() {
        match r.read(&mut buf[total..]) {
            Ok(0) => {
                return Ok(if total == 0 {
                    ReadExact::Eof
                } else {
                    ReadExact::Partial
                });
            }
            Ok(n) => total += n,
            Err(ref e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        }
    }
    Ok(ReadExact::Ok)
}

// ---------------------------------------------------------------------------
// WalWriter
// ---------------------------------------------------------------------------

/// Single-writer WAL engine. NOT `Send` — enforces single-writer at the type level.
pub struct WalWriter {
    dir: PathBuf,
    config: WalConfig,
    current_segment_id: u64,
    writer: BufWriter<File>,
    next_sequence: u64,
    segment_bytes_written: u64,
    segment_entry_count: u32,
}

// The PhantomData<*mut ()> field prevents WalWriter from being Send.
// WalWriter is Send — protected by Mutex in ChunkManager.

impl WalWriter {
    /// Open or create a WAL at the given directory path.
    ///
    /// If the directory already contains segments, the writer resumes after the last
    /// valid entry across all segments (scanning to find `next_sequence`).
    pub fn open(dir: &Path, config: WalConfig) -> WalResult<Self> {
        fs::create_dir_all(dir)?;

        let segments = list_segments(dir)?;

        // Determine next_sequence by replaying existing segments.
        let mut next_sequence: u64 = 1;
        for (_seg_id, path) in &segments {
            let mut file = BufReader::new(File::open(path)?);
            let _seg_id = read_segment_header(&mut file)?;
            loop {
                match read_entry(&mut file) {
                    Ok(Some(entry)) => {
                        if entry.sequence >= next_sequence {
                            next_sequence = entry.sequence + 1;
                        }
                    }
                    Ok(None) => break,                  // EOF or partial
                    Err(WalError::Corrupt(_)) => break, // stop at corruption
                    Err(e) => return Err(e),
                }
            }
        }

        // Determine the segment_id for the new (or continued) segment.
        let new_segment_id = segments.last().map(|(id, _)| id + 1).unwrap_or(0);

        let (writer, segment_bytes) = create_segment(dir, new_segment_id)?;

        Ok(Self {
            dir: dir.to_path_buf(),
            config,
            current_segment_id: new_segment_id,
            writer,
            next_sequence,
            segment_bytes_written: segment_bytes,
            segment_entry_count: 0,
        })
    }

    /// Append an operation. Returns the assigned sequence number.
    pub fn append(&mut self, op: WalOperation) -> WalResult<u64> {
        let payload = bincode::serialize(&op)?;
        let seq = self.next_sequence;

        // Rotate if the current segment exceeds the configured limit.
        let entry_size = ENTRY_HEADER_SIZE as u64 + payload.len() as u64;
        if self.segment_bytes_written + entry_size > self.config.max_segment_bytes
            && self.segment_entry_count > 0
        {
            self.rotate()?;
        }

        write_entry(&mut self.writer, seq, &payload)?;

        self.next_sequence = seq + 1;
        self.segment_bytes_written += entry_size;
        self.segment_entry_count += 1;

        if self.config.sync_on_append {
            self.sync()?;
        }

        Ok(seq)
    }

    /// Fsync the current segment to ensure durability.
    pub fn sync(&mut self) -> WalResult<()> {
        self.writer.flush()?;
        self.writer.get_ref().sync_data()?;
        Ok(())
    }

    /// Write a checkpoint marker and return the checkpoint sequence.
    pub fn checkpoint(&mut self) -> WalResult<u64> {
        let seq = self.next_sequence;
        self.append(WalOperation::Checkpoint { sequence: seq })
    }

    /// Rotate to a new segment file. Closes the current segment (writing footer) and opens a new one.
    pub fn rotate(&mut self) -> WalResult<()> {
        // Flush and write footer for current segment.
        let last_seq = self.next_sequence.saturating_sub(1);
        self.writer.flush()?;
        write_segment_footer(&mut self.writer, self.segment_entry_count, last_seq)?;
        self.writer.flush()?;
        self.writer.get_ref().sync_data()?;

        // Open new segment.
        self.current_segment_id += 1;
        let (writer, segment_bytes) = create_segment(&self.dir, self.current_segment_id)?;
        self.writer = writer;
        self.segment_bytes_written = segment_bytes;
        self.segment_entry_count = 0;

        Ok(())
    }

    /// Close the WAL cleanly (writes footer and syncs).
    pub fn close(mut self) -> WalResult<()> {
        let last_seq = self.next_sequence.saturating_sub(1);
        self.writer.flush()?;
        write_segment_footer(&mut self.writer, self.segment_entry_count, last_seq)?;
        self.writer.flush()?;
        self.writer.get_ref().sync_all()?;
        Ok(())
    }

    /// Return the next sequence number that will be assigned.
    pub fn next_sequence(&self) -> u64 {
        self.next_sequence
    }
}

/// Create a new segment file, write its header, and return a BufWriter plus the initial byte count.
fn create_segment(dir: &Path, segment_id: u64) -> WalResult<(BufWriter<File>, u64)> {
    let path = dir.join(segment_filename(segment_id));
    let file = OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(&path)?;
    let mut writer = BufWriter::new(file);
    write_segment_header(&mut writer, segment_id)?;
    writer.flush()?;
    Ok((writer, SEGMENT_HEADER_SIZE))
}

// ---------------------------------------------------------------------------
// WalReader
// ---------------------------------------------------------------------------

/// Read-only WAL replay engine.
pub struct WalReader {
    dir: PathBuf,
}

impl WalReader {
    /// Open a WAL directory for reading.
    pub fn open(dir: &Path) -> WalResult<Self> {
        Ok(Self {
            dir: dir.to_path_buf(),
        })
    }

    /// Replay all entries from the given sequence number (inclusive).
    ///
    /// Calls `f` for each valid entry with sequence >= `from_sequence`.
    /// Stops on CRC mismatch (corruption boundary) or EOF.
    /// Returns the last valid sequence number seen, or 0 if none.
    pub fn replay_from<F>(&self, from_sequence: u64, mut f: F) -> WalResult<u64>
    where
        F: FnMut(WalEntry) -> WalResult<()>,
    {
        let segments = list_segments(&self.dir)?;
        let mut last_valid_seq: u64 = 0;

        for (_seg_id, path) in &segments {
            let file = File::open(path)?;
            let file_len = file.metadata()?.len();
            let mut reader = BufReader::new(file);

            // Read and validate segment header.
            if file_len < SEGMENT_HEADER_SIZE {
                continue; // skip truncated segment
            }
            match read_segment_header(&mut reader) {
                Ok(_) => {}
                Err(WalError::InvalidMagic) => continue,
                Err(e) => return Err(e),
            }

            loop {
                match read_entry(&mut reader) {
                    Ok(Some(entry)) => {
                        last_valid_seq = entry.sequence;
                        if entry.sequence >= from_sequence {
                            f(entry)?;
                        }
                    }
                    Ok(None) => break,
                    Err(WalError::Corrupt(_)) => {
                        // Corruption boundary: stop replay entirely.
                        return Ok(last_valid_seq);
                    }
                    Err(e) => return Err(e),
                }
            }
        }

        Ok(last_valid_seq)
    }

    /// Find the latest checkpoint sequence number, or `None` if no checkpoint exists.
    pub fn find_latest_checkpoint(&self) -> WalResult<Option<u64>> {
        let mut latest: Option<u64> = None;

        self.replay_from(1, |entry| {
            if let WalOperation::Checkpoint { sequence } = &entry.operation {
                latest = Some(*sequence);
            }
            Ok(())
        })?;

        Ok(latest)
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn tmp_dir() -> TempDir {
        tempfile::tempdir().expect("create temp dir")
    }

    fn default_config() -> WalConfig {
        WalConfig::default()
    }

    // 1. Basic roundtrip
    #[test]
    fn test_basic_roundtrip() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for i in 0..10 {
            let seq = writer
                .append(WalOperation::PutObject {
                    object_id: format!("obj-{i}"),
                    size: i * 100,
                    etag: format!("etag-{i}"),
                })
                .unwrap();
            assert_eq!(seq, i + 1);
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut entries = Vec::new();
        let last = reader
            .replay_from(1, |e| {
                entries.push(e);
                Ok(())
            })
            .unwrap();

        assert_eq!(entries.len(), 10);
        assert_eq!(last, 10);

        for (i, entry) in entries.iter().enumerate() {
            assert_eq!(entry.sequence, i as u64 + 1);
            match &entry.operation {
                WalOperation::PutObject {
                    object_id, size, ..
                } => {
                    assert_eq!(object_id, &format!("obj-{i}"));
                    assert_eq!(*size, i as u64 * 100);
                }
                other => panic!("unexpected operation: {other:?}"),
            }
        }
    }

    // 2. Sequence numbers are gapless
    #[test]
    fn test_sequence_gapless() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        let mut seqs = Vec::new();
        for _ in 0..50 {
            seqs.push(writer.append(WalOperation::Noop).unwrap());
        }
        writer.close().unwrap();

        let expected: Vec<u64> = (1..=50).collect();
        assert_eq!(seqs, expected);

        // Verify on replay too.
        let reader = WalReader::open(dir.path()).unwrap();
        let mut replayed_seqs = Vec::new();
        reader
            .replay_from(1, |e| {
                replayed_seqs.push(e.sequence);
                Ok(())
            })
            .unwrap();
        assert_eq!(replayed_seqs, expected);
    }

    // 3. Corruption detection
    #[test]
    fn test_corruption_detection() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for i in 0..5 {
            writer
                .append(WalOperation::PutObject {
                    object_id: format!("obj-{i}"),
                    size: 0,
                    etag: String::new(),
                })
                .unwrap();
        }
        writer.close().unwrap();

        // Corrupt a byte in the middle of the segment file.
        let segments = list_segments(dir.path()).unwrap();
        let seg_path = &segments[0].1;
        let mut data = fs::read(seg_path).unwrap();
        // Corrupt the CRC of the 3rd entry. Each entry: [len:4][seq:8][crc:4][payload].
        // Entry payloads are ~20 bytes each. We target the CRC field of entry 3.
        // Offset: header(64) + entry1(~36) + entry2(~36) + 4(len) + 8(seq) = ~148
        // Actually, find the 3rd entry's CRC by searching for its pattern.
        // Simplest: corrupt byte right after segment header + some entries.
        // The CRC field is at offset 12 within each entry (after len+seq).
        let corrupt_offset = SEGMENT_HEADER_SIZE as usize + 100; // Middle of entry 2-3
        if corrupt_offset + 4 < data.len() {
            // Corrupt 4 bytes to ensure CRC mismatch is detected.
            data[corrupt_offset] ^= 0xFF;
            data[corrupt_offset + 1] ^= 0xFF;
            data[corrupt_offset + 2] ^= 0xFF;
            data[corrupt_offset + 3] ^= 0xFF;
        }
        fs::write(seg_path, &data).unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut entries = Vec::new();
        let last = reader
            .replay_from(1, |e| {
                entries.push(e);
                Ok(())
            })
            .unwrap();

        // We should get fewer than 5 entries — replay stops at corruption.
        assert!(
            entries.len() < 5,
            "expected replay to stop early, got {} entries",
            entries.len()
        );
        assert_eq!(last, entries.last().map(|e| e.sequence).unwrap_or(0));
    }

    // 4. Segment rotation
    #[test]
    fn test_segment_rotation() {
        let dir = tmp_dir();
        let config = WalConfig {
            max_segment_bytes: 256, // tiny segments to force rotation
            sync_on_append: false,
        };
        let mut writer = WalWriter::open(dir.path(), config).unwrap();

        for i in 0..20 {
            writer
                .append(WalOperation::PutObject {
                    object_id: format!("object-{i}"),
                    size: i,
                    etag: String::new(),
                })
                .unwrap();
        }
        writer.close().unwrap();

        // Should have multiple segments.
        let segments = list_segments(dir.path()).unwrap();
        assert!(
            segments.len() > 1,
            "expected multiple segments, got {}",
            segments.len()
        );

        // Replay should see all 20 entries.
        let reader = WalReader::open(dir.path()).unwrap();
        let mut count = 0u64;
        let last = reader
            .replay_from(1, |e| {
                count += 1;
                assert_eq!(e.sequence, count);
                Ok(())
            })
            .unwrap();

        assert_eq!(count, 20);
        assert_eq!(last, 20);
    }

    // 5. Checkpoint
    #[test]
    fn test_checkpoint() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for _ in 0..5 {
            writer.append(WalOperation::Noop).unwrap();
        }
        let ckpt_seq = writer.checkpoint().unwrap();
        assert_eq!(ckpt_seq, 6);

        for _ in 0..5 {
            writer.append(WalOperation::Noop).unwrap();
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let latest = reader.find_latest_checkpoint().unwrap();
        assert_eq!(latest, Some(6));
    }

    // 6. Empty WAL
    #[test]
    fn test_empty_wal() {
        let dir = tmp_dir();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut count = 0;
        let last = reader
            .replay_from(1, |_| {
                count += 1;
                Ok(())
            })
            .unwrap();

        assert_eq!(count, 0);
        assert_eq!(last, 0);

        let ckpt = reader.find_latest_checkpoint().unwrap();
        assert_eq!(ckpt, None);
    }

    // 7. Partial write simulation
    #[test]
    fn test_partial_write() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for i in 0..3 {
            writer
                .append(WalOperation::PutObject {
                    object_id: format!("obj-{i}"),
                    size: 0,
                    etag: String::new(),
                })
                .unwrap();
        }
        // Flush but don't close (no footer).
        writer.sync().unwrap();

        // Now manually truncate the file to simulate a partial 4th entry.
        let segments = list_segments(dir.path()).unwrap();
        let seg_path = &segments.last().unwrap().1;
        let mut file = OpenOptions::new().append(true).open(seg_path).unwrap();
        // Write a partial entry header (only 8 of 16 bytes).
        file.write_all(&[0u8; 8]).unwrap();
        file.sync_all().unwrap();
        drop(file);

        let reader = WalReader::open(dir.path()).unwrap();
        let mut entries = Vec::new();
        let last = reader
            .replay_from(1, |e| {
                entries.push(e);
                Ok(())
            })
            .unwrap();

        assert_eq!(entries.len(), 3);
        assert_eq!(last, 3);
    }

    // 8. Large entries
    #[test]
    fn test_large_entry() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        let big_name = "x".repeat(1_000_000); // ~1 MB
        let seq = writer
            .append(WalOperation::PutObject {
                object_id: big_name.clone(),
                size: 42,
                etag: "big".to_string(),
            })
            .unwrap();
        assert_eq!(seq, 1);
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut entries = Vec::new();
        reader
            .replay_from(1, |e| {
                entries.push(e);
                Ok(())
            })
            .unwrap();

        assert_eq!(entries.len(), 1);
        match &entries[0].operation {
            WalOperation::PutObject { object_id, .. } => {
                assert_eq!(object_id, &big_name);
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    // 9. Many entries
    #[test]
    fn test_many_entries() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for i in 0..10_000 {
            let seq = writer
                .append(WalOperation::Truncate {
                    inode: i,
                    size: i * 2,
                })
                .unwrap();
            assert_eq!(seq, i + 1);
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut count = 0u64;
        let last = reader
            .replay_from(1, |e| {
                count += 1;
                assert_eq!(e.sequence, count);
                Ok(())
            })
            .unwrap();

        assert_eq!(count, 10_000);
        assert_eq!(last, 10_000);
    }

    // 10. All WalOperation variants serialize/deserialize correctly
    #[test]
    fn test_all_operation_variants() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        let ops = vec![
            WalOperation::CreateVolume {
                volume_id: [1u8; 16],
                name: "vol0".into(),
                size: 1024,
                replication: 3,
            },
            WalOperation::DeleteVolume {
                volume_id: [2u8; 16],
            },
            WalOperation::CreateFile {
                volume_id: [3u8; 16],
                path: "/foo/bar".into(),
                inode: 42,
            },
            WalOperation::WriteChunks {
                inode: 99,
                offset: 4096,
                chunks: vec![[0xAB; 32], [0xCD; 32]],
            },
            WalOperation::Truncate {
                inode: 10,
                size: 500,
            },
            WalOperation::Delete {
                volume_id: [4u8; 16],
                path: "/to/delete".into(),
            },
            WalOperation::Rename {
                volume_id: [5u8; 16],
                from: "/old".into(),
                to: "/new".into(),
            },
            WalOperation::CreateBlob {
                id: [0xDE; 32],
                chunks: vec![[0x11; 32]],
            },
            WalOperation::DeleteBlob { id: [0xEF; 32] },
            WalOperation::PutObject {
                object_id: "bucket/key".into(),
                size: 999,
                etag: "abc123".into(),
            },
            WalOperation::DeleteObject {
                object_id: "bucket/gone".into(),
            },
            WalOperation::Checkpoint { sequence: 0 },
            WalOperation::Noop,
        ];

        for op in &ops {
            writer.append(op.clone()).unwrap();
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut replayed = Vec::new();
        reader
            .replay_from(1, |e| {
                replayed.push(e.operation);
                Ok(())
            })
            .unwrap();

        assert_eq!(replayed.len(), ops.len());
        for (orig, replayed) in ops.iter().zip(replayed.iter()) {
            assert_eq!(orig, replayed);
        }
    }

    // 11. Replay from offset
    #[test]
    fn test_replay_from_offset() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for _ in 0..100 {
            writer.append(WalOperation::Noop).unwrap();
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut entries = Vec::new();
        let last = reader
            .replay_from(50, |e| {
                entries.push(e.sequence);
                Ok(())
            })
            .unwrap();

        assert_eq!(last, 100);
        assert_eq!(entries.len(), 51); // sequences 50..=100
        assert_eq!(entries[0], 50);
        assert_eq!(*entries.last().unwrap(), 100);
    }

    // 12. WalWriter is Send (protected by Mutex in ChunkManager)
    #[test]
    fn test_writer_is_send() {
        fn assert_send<T: Send>() {}
        assert_send::<WalWriter>();
    }

    // 13. Reopen after unclean shutdown (no footer)
    #[test]
    fn test_reopen_after_unclean_shutdown() {
        let dir = tmp_dir();

        // Write entries but don't call close() (no footer).
        {
            let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();
            for i in 0..5 {
                writer
                    .append(WalOperation::Truncate {
                        inode: i,
                        size: i * 10,
                    })
                    .unwrap();
            }
            writer.sync().unwrap();
            // Drop without close — simulates crash.
        }

        // Reopen writer — should resume from sequence 6.
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();
        assert_eq!(writer.next_sequence(), 6);

        let seq = writer.append(WalOperation::Noop).unwrap();
        assert_eq!(seq, 6);
        writer.close().unwrap();

        // Reader sees all 6 entries across old + new segments.
        let reader = WalReader::open(dir.path()).unwrap();
        let mut count = 0;
        reader
            .replay_from(1, |_| {
                count += 1;
                Ok(())
            })
            .unwrap();
        assert_eq!(count, 6);
    }

    // 14. Segment rotation preserves gapless sequences across segments
    #[test]
    fn test_rotation_gapless_across_segments() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        for _ in 0..5 {
            writer.append(WalOperation::Noop).unwrap();
        }
        writer.rotate().unwrap();
        for _ in 0..5 {
            writer.append(WalOperation::Noop).unwrap();
        }
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut seqs = Vec::new();
        reader
            .replay_from(1, |e| {
                seqs.push(e.sequence);
                Ok(())
            })
            .unwrap();

        let expected: Vec<u64> = (1..=10).collect();
        assert_eq!(seqs, expected);
    }

    // 15. Multiple checkpoints — find_latest_checkpoint returns the last one
    #[test]
    fn test_multiple_checkpoints() {
        let dir = tmp_dir();
        let mut writer = WalWriter::open(dir.path(), default_config()).unwrap();

        writer.append(WalOperation::Noop).unwrap();
        writer.checkpoint().unwrap(); // seq 2
        writer.append(WalOperation::Noop).unwrap();
        writer.append(WalOperation::Noop).unwrap();
        writer.checkpoint().unwrap(); // seq 5
        writer.append(WalOperation::Noop).unwrap();
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let latest = reader.find_latest_checkpoint().unwrap();
        assert_eq!(latest, Some(5));
    }

    // 16. sync_on_append config
    #[test]
    fn test_sync_on_append() {
        let dir = tmp_dir();
        let config = WalConfig {
            max_segment_bytes: DEFAULT_MAX_SEGMENT_BYTES,
            sync_on_append: true,
        };
        let mut writer = WalWriter::open(dir.path(), config).unwrap();

        // Should not panic; sync happens inside append.
        writer.append(WalOperation::Noop).unwrap();
        writer.append(WalOperation::Noop).unwrap();
        writer.close().unwrap();

        let reader = WalReader::open(dir.path()).unwrap();
        let mut count = 0;
        reader
            .replay_from(1, |_| {
                count += 1;
                Ok(())
            })
            .unwrap();
        assert_eq!(count, 2);
    }
}
