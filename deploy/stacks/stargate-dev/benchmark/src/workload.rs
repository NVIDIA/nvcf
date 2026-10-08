// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::fs;
use std::io::{BufWriter, Write};
use std::path::Path;

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tempfile::NamedTempFile;

const HEADER: &[u8] = b"version: 1\nscenarios:\n  - name: normal\n    mode: normal\n    prompts:\n";

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "kebab-case",
    rename_all_fields = "camelCase"
)]
pub enum Workload {
    Unique {
        count: usize,
        bytes: usize,
        start: u64,
    },
    Sessions {
        sessions: usize,
        turns: usize,
        stable_prefix_bytes: usize,
        turn_bytes: usize,
    },
    SessionWorkers {
        workers: usize,
        session_tasks: Vec<Vec<usize>>,
    },
    Hot {
        count: usize,
        minimum: usize,
        maximum: usize,
    },
    Short {
        count: usize,
        sizes: Vec<usize>,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Fingerprint {
    pub prompt_count: usize,
    pub sha256: String,
}

impl Workload {
    pub fn prompt_count(&self) -> Result<usize> {
        let count = match self {
            Self::Unique { count, .. } | Self::Hot { count, .. } | Self::Short { count, .. } => {
                *count
            }
            Self::Sessions {
                sessions, turns, ..
            } => sessions
                .checked_mul(*turns)
                .context("session prompt count overflow")?,
            Self::SessionWorkers {
                workers,
                session_tasks,
            } => workers
                .checked_mul(task_turn_count(session_tasks)?)
                .context("worker prompt count overflow")?,
        };
        ensure!(count > 0, "workload must contain at least one prompt");
        Ok(count)
    }

    pub fn validate(&self) -> Result<()> {
        self.prompt_count()?;
        match self {
            Self::Unique {
                count,
                bytes,
                start,
            } => {
                let last = start
                    .checked_add(u64::try_from(count - 1).context("unique prompt count overflow")?)
                    .context("unique prompt ID overflow")?;
                ensure!(
                    *bytes >= format!("canonical-unique-session={last:08}; ").len(),
                    "unique prompt size is smaller than its identity prefix"
                );
                validate_prompt_size(*bytes)?;
            }
            Self::Sessions {
                sessions,
                turns,
                stable_prefix_bytes,
                turn_bytes,
            } => {
                ensure!(
                    *stable_prefix_bytes
                        >= format!("canonical-session={:08}; ", sessions - 1).len(),
                    "stable prefix size is smaller than its session identity"
                );
                ensure!(
                    *turn_bytes >= format!(" turn={:04}; ", turns - 1).len(),
                    "turn size is smaller than its turn prefix"
                );
                let maximum = turn_bytes
                    .checked_mul(*turns)
                    .and_then(|growth| stable_prefix_bytes.checked_add(growth))
                    .context("session prompt size overflow")?;
                validate_prompt_size(maximum)?;
            }
            Self::SessionWorkers { session_tasks, .. } => {
                for task in session_tasks {
                    ensure!(
                        !task.is_empty(),
                        "session tasks must contain at least one turn"
                    );
                    let mut previous = 256;
                    for tokens in task {
                        let bytes = tokens
                            .checked_sub(5)
                            .and_then(|tokens| tokens.checked_mul(4))
                            .context("session input token size overflow or underflow")?;
                        ensure!(
                            bytes >= previous,
                            "session worker prompts must retain their 256-byte prefix and never shrink"
                        );
                        validate_prompt_size(bytes)?;
                        previous = bytes;
                    }
                }
            }
            Self::Hot {
                minimum, maximum, ..
            } => {
                ensure!(
                    *minimum >= 256,
                    "hot prompts must retain their 256-byte prefix"
                );
                ensure!(maximum >= minimum, "hot prompt sizes must not decrease");
                validate_prompt_size(*maximum)?;
            }
            Self::Short { sizes, .. } => {
                ensure!(!sizes.is_empty(), "short sessions must define prompt sizes");
                let mut previous = 256;
                for bytes in sizes {
                    ensure!(
                        *bytes >= previous,
                        "short session prompts must retain their 256-byte prefix and never shrink"
                    );
                    validate_prompt_size(*bytes)?;
                    previous = *bytes;
                }
            }
        }
        Ok(())
    }

    pub fn write(&self, path: &Path) -> Result<Fingerprint> {
        self.validate()?;
        let sidecar_path = path.with_extension("json");
        ensure!(
            path != sidecar_path,
            "workload and fingerprint paths must differ"
        );
        ensure!(path.file_name().is_some(), "workload path must name a file");
        let parent = path
            .parent()
            .filter(|parent| !parent.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."));
        fs::create_dir_all(parent)
            .with_context(|| format!("create workload directory {}", parent.display()))?;
        let mut writer = WorkloadWriter {
            output: BufWriter::new(NamedTempFile::new_in(parent)?),
            hasher: Sha256::new(),
        };
        writer.write_all(HEADER)?;
        self.write_prompts(&mut writer)
            .with_context(|| format!("write workload {}", path.display()))?;
        writer.flush()?;
        let fingerprint = Fingerprint {
            prompt_count: self.prompt_count()?,
            sha256: format!("{:x}", writer.hasher.finalize()),
        };
        let workload_file = writer.output.into_inner()?;
        let mut fingerprint_file = NamedTempFile::new_in(parent)?;
        serde_json::to_writer_pretty(&mut fingerprint_file, &fingerprint)?;
        fingerprint_file.write_all(b"\n")?;
        fingerprint_file.flush()?;
        workload_file
            .persist(path)
            .with_context(|| format!("replace workload {}", path.display()))?;
        fingerprint_file
            .persist(&sidecar_path)
            .with_context(|| format!("replace workload fingerprint {}", sidecar_path.display()))?;
        Ok(fingerprint)
    }

    pub fn fingerprint(&self) -> Result<Fingerprint> {
        self.validate()?;
        let mut writer = WorkloadWriter {
            output: std::io::sink(),
            hasher: Sha256::new(),
        };
        writer.write_all(HEADER)?;
        self.write_prompts(&mut writer)?;
        Ok(Fingerprint {
            prompt_count: self.prompt_count()?,
            sha256: format!("{:x}", writer.hasher.finalize()),
        })
    }

    fn write_prompts(&self, writer: &mut impl Write) -> Result<()> {
        match self {
            Self::Unique {
                count,
                bytes,
                start,
            } => {
                for index in 0..*count {
                    let mut prompt =
                        format!("canonical-unique-session={:08}; ", start + index as u64);
                    extend_prompt(
                        &mut prompt,
                        "Unique request context that must not share a cache affinity key. ",
                        *bytes,
                    )?;
                    write_prompt(writer, &prompt)?;
                }
            }
            Self::Sessions {
                sessions,
                turns,
                stable_prefix_bytes,
                turn_bytes,
            } => {
                let mut histories = Vec::new();
                histories.try_reserve_exact(*sessions)?;
                for session in 0..*sessions {
                    let mut history = format!("canonical-session={session:08}; ");
                    extend_prompt(
                        &mut history,
                        "Stable system and document context for this conversation. ",
                        *stable_prefix_bytes,
                    )?;
                    histories.push(history);
                }
                for turn in 0..*turns {
                    let mut suffix = format!(" turn={turn:04}; ");
                    extend_prompt(
                        &mut suffix,
                        "New information appended during this turn. ",
                        *turn_bytes,
                    )?;
                    for history in &mut histories {
                        history.try_reserve(suffix.len())?;
                        history.push_str(&suffix);
                        write_prompt(writer, history)?;
                    }
                }
            }
            Self::SessionWorkers {
                workers,
                session_tasks,
            } => {
                struct Worker {
                    task: usize,
                    turn: usize,
                    history: String,
                }
                let mut histories = Vec::new();
                histories.try_reserve_exact(*workers)?;
                histories.resize_with(*workers, || Worker {
                    task: 0,
                    turn: 0,
                    history: String::new(),
                });
                for _ in 0..task_turn_count(session_tasks)? {
                    for (index, worker) in histories.iter_mut().enumerate() {
                        let task = &session_tasks
                            [(index % session_tasks.len() + worker.task) % session_tasks.len()];
                        if worker.turn == 0 {
                            let session_id = Sha256::digest(
                                format!("canonical-session-worker={index};task={}", worker.task)
                                    .as_bytes(),
                            );
                            worker.history = format!("canonical-session={session_id:x}; ");
                            extend_prompt(
                                &mut worker.history,
                                "Stable repository and coding context. ",
                                256,
                            )?;
                        }
                        extend_prompt(
                            &mut worker.history,
                            " Additional source code, build output, and conversation context. ",
                            (task[worker.turn] - 5) * 4,
                        )?;
                        write_prompt(writer, &worker.history)?;
                        worker.turn += 1;
                        if worker.turn == task.len() {
                            worker.turn = 0;
                            worker.task += 1;
                        }
                    }
                }
            }
            Self::Hot {
                count,
                minimum,
                maximum,
            } => {
                let mut history = "canonical-hot-session; ".to_owned();
                extend_prompt(
                    &mut history,
                    "Stable conversation identity retained across every turn. ",
                    256,
                )?;
                for index in 0..*count {
                    let growth = ((*maximum - *minimum) as u128 * index as u128
                        / (count - 1).max(1) as u128) as usize;
                    extend_prompt(
                        &mut history,
                        &format!(
                            " Long-session turn {index:05} adds durable conversation history. "
                        ),
                        minimum + growth,
                    )?;
                    write_prompt(writer, &history)?;
                }
            }
            Self::Short { count, sizes } => {
                let mut emitted = 0;
                let mut session = 0;
                while emitted < *count {
                    let mut history = format!("canonical-short-session={session:08}; ");
                    extend_prompt(&mut history, "Stable short conversation identity. ", 256)?;
                    for (turn, bytes) in sizes.iter().take(1 + session % sizes.len()).enumerate() {
                        if emitted == *count {
                            break;
                        }
                        extend_prompt(
                            &mut history,
                            &format!(
                                " Short-session turn {} contains a user exchange. ",
                                turn + 1
                            ),
                            *bytes,
                        )?;
                        write_prompt(writer, &history)?;
                        emitted += 1;
                    }
                    session += 1;
                }
            }
        }
        Ok(())
    }
}

fn task_turn_count(tasks: &[Vec<usize>]) -> Result<usize> {
    tasks.iter().try_fold(0_usize, |count, task| {
        count
            .checked_add(task.len())
            .context("session task turn count overflow")
    })
}

fn validate_prompt_size(bytes: usize) -> Result<()> {
    ensure!(
        bytes <= isize::MAX as usize,
        "prompt size exceeds the string capacity limit"
    );
    Ok(())
}

fn extend_prompt(prompt: &mut String, phrase: &str, bytes: usize) -> Result<()> {
    let remaining = bytes
        .checked_sub(prompt.len())
        .context("prompt history would shrink")?;
    prompt.try_reserve(remaining)?;
    while prompt.len() < bytes {
        prompt.push_str(&phrase[..phrase.len().min(bytes - prompt.len())]);
    }
    Ok(())
}

fn write_prompt(writer: &mut impl Write, prompt: &str) -> Result<()> {
    writer.write_all(b"      - ")?;
    serde_json::to_writer(&mut *writer, prompt)?;
    writer.write_all(b"\n")?;
    Ok(())
}

struct WorkloadWriter<W> {
    output: W,
    hasher: Sha256,
}

impl<W: Write> Write for WorkloadWriter<W> {
    fn write(&mut self, buffer: &[u8]) -> std::io::Result<usize> {
        let written = self.output.write(buffer)?;
        self.hasher.update(&buffer[..written]);
        Ok(written)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        self.output.flush()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    fn prompts(workload: &Workload) -> Vec<String> {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("workload.yaml");
        workload.write(&path).unwrap();
        let value: serde_yaml_ng::Value =
            serde_yaml_ng::from_slice(&fs::read(path).unwrap()).unwrap();
        value["scenarios"][0]["prompts"]
            .as_sequence()
            .unwrap()
            .iter()
            .map(|prompt| prompt.as_str().unwrap().to_owned())
            .collect()
    }

    #[test]
    fn yaml_bytes_match_corrected_python_workloads() {
        // SHA-256 of complete YAML from loadtest.py at 1b610889c, generated once.
        let cases = [
            (
                Workload::Unique {
                    count: 3,
                    bytes: 64,
                    start: 9,
                },
                3,
                "051258af6cd5bff86b1937f83d5eddbe6f7794332b2db2ceaeeda84b862429b2",
            ),
            (
                Workload::Sessions {
                    sessions: 2,
                    turns: 3,
                    stable_prefix_bytes: 128,
                    turn_bytes: 64,
                },
                6,
                "6cb30ec8211dba8e844d8ecc9ba7169adabe0a75d32d725c601c7f63249c6c5d",
            ),
            (
                Workload::SessionWorkers {
                    workers: 3,
                    session_tasks: vec![vec![80, 90], vec![70]],
                },
                9,
                "961cf2ed8c5a90324833eaeb9a70a8ce3fa69d6174c8d61c82d230a0f390a164",
            ),
            (
                Workload::Hot {
                    count: 4,
                    minimum: 256,
                    maximum: 350,
                },
                4,
                "fde0a8860934306fecc3e5c275ccf1d872b55f88dbb3b2e0ec3e1ed2d6cbeaaf",
            ),
            (
                Workload::Short {
                    count: 8,
                    sizes: vec![256, 300, 360],
                },
                8,
                "5ab8b7eb9bffde8a52c37fbd39f6ee1bbfe16e0dc39646d8b6245d83ff6a8bac",
            ),
        ];
        for (workload, count, expected_hash) in cases {
            let directory = tempfile::tempdir().unwrap();
            let path = directory.path().join("workload.yaml");
            let sidecar = path.with_extension("json");
            fs::write(&path, "old workload").unwrap();
            fs::write(&sidecar, "old fingerprint").unwrap();
            let fingerprint = workload.write(&path).unwrap();
            assert_eq!(workload.fingerprint().unwrap(), fingerprint);
            let contents = fs::read(&path).unwrap();
            assert_eq!(fingerprint.prompt_count, count);
            assert_eq!(fingerprint.sha256, expected_hash);
            assert_eq!(format!("{:x}", Sha256::digest(&contents)), expected_hash);
            assert!(contents.starts_with(HEADER));
            assert_eq!(contents.last(), Some(&b'\n'));
            assert_eq!(
                serde_json::from_slice::<Fingerprint>(&fs::read(sidecar).unwrap()).unwrap(),
                fingerprint
            );
        }
    }

    #[test]
    fn session_order_preserves_affinity_and_accumulated_history() {
        let sessions = prompts(&Workload::Sessions {
            sessions: 2,
            turns: 3,
            stable_prefix_bytes: 512,
            turn_bytes: 128,
        });
        assert_eq!(
            sessions.iter().map(String::len).collect::<Vec<_>>(),
            [640, 640, 768, 768, 896, 896]
        );
        assert_ne!(&sessions[0][..256], &sessions[1][..256]);
        for index in 2..sessions.len() {
            assert!(sessions[index].starts_with(&sessions[index - 2]));
        }
        let workers = prompts(&Workload::SessionWorkers {
            workers: 2,
            session_tasks: vec![vec![80_000, 100_000], vec![8_000, 16_000]],
        });
        assert_eq!(
            workers
                .iter()
                .step_by(2)
                .map(|s| 5 + s.len() / 4)
                .collect::<Vec<_>>(),
            [80_000, 100_000, 8_000, 16_000]
        );
        assert_eq!(
            workers
                .iter()
                .skip(1)
                .step_by(2)
                .map(|s| 5 + s.len() / 4)
                .collect::<Vec<_>>(),
            [8_000, 16_000, 80_000, 100_000]
        );
        assert_eq!(
            workers
                .iter()
                .map(|s| &s[..256])
                .collect::<HashSet<_>>()
                .len(),
            4
        );
        for index in [2, 3, 6, 7] {
            assert!(workers[index].starts_with(&workers[index - 2]));
        }
    }

    #[test]
    fn mixed_workloads_preserve_growth_and_session_boundaries() {
        let hot = prompts(&Workload::Hot {
            count: 4,
            minimum: 512,
            maximum: 1024,
        });
        assert_eq!(
            hot.iter().map(String::len).collect::<Vec<_>>(),
            [512, 682, 853, 1024]
        );
        for pair in hot.windows(2) {
            assert!(pair[1].starts_with(&pair[0]));
        }
        let short = prompts(&Workload::Short {
            count: 8,
            sizes: vec![512, 768, 1024],
        });
        assert_eq!(
            short.iter().map(String::len).collect::<Vec<_>>(),
            [512, 512, 768, 512, 768, 1024, 512, 512]
        );
        for (later, earlier) in [(2, 1), (4, 3), (5, 4)] {
            assert!(short[later].starts_with(&short[earlier]));
        }
        assert_eq!(
            short
                .iter()
                .map(|s| &s[..256])
                .collect::<HashSet<_>>()
                .len(),
            5
        );
    }

    #[test]
    fn invalid_recipes_do_not_replace_existing_artifacts() {
        let invalid = [
            Workload::Unique {
                count: 0,
                bytes: 512,
                start: 0,
            },
            Workload::Unique {
                count: 2,
                bytes: 512,
                start: u64::MAX,
            },
            Workload::Unique {
                count: 1,
                bytes: 1,
                start: 0,
            },
            Workload::Unique {
                count: 1,
                bytes: usize::MAX,
                start: 0,
            },
            Workload::Sessions {
                sessions: 0,
                turns: 1,
                stable_prefix_bytes: 512,
                turn_bytes: 128,
            },
            Workload::Sessions {
                sessions: 1,
                turns: 0,
                stable_prefix_bytes: 512,
                turn_bytes: 128,
            },
            Workload::Sessions {
                sessions: usize::MAX,
                turns: 2,
                stable_prefix_bytes: 512,
                turn_bytes: 128,
            },
            Workload::Sessions {
                sessions: 1,
                turns: usize::MAX,
                stable_prefix_bytes: 512,
                turn_bytes: 128,
            },
            Workload::Sessions {
                sessions: 1,
                turns: 1,
                stable_prefix_bytes: 1,
                turn_bytes: 128,
            },
            Workload::Sessions {
                sessions: 1,
                turns: 1,
                stable_prefix_bytes: 512,
                turn_bytes: 1,
            },
            Workload::SessionWorkers {
                workers: 0,
                session_tasks: vec![vec![80]],
            },
            Workload::SessionWorkers {
                workers: 1,
                session_tasks: vec![],
            },
            Workload::SessionWorkers {
                workers: 1,
                session_tasks: vec![vec![], vec![80]],
            },
            Workload::SessionWorkers {
                workers: usize::MAX,
                session_tasks: vec![vec![80, 90]],
            },
            Workload::SessionWorkers {
                workers: 1,
                session_tasks: vec![vec![5]],
            },
            Workload::SessionWorkers {
                workers: 1,
                session_tasks: vec![vec![usize::MAX]],
            },
            Workload::SessionWorkers {
                workers: 1,
                session_tasks: vec![vec![80, 79]],
            },
            Workload::Hot {
                count: 0,
                minimum: 256,
                maximum: 512,
            },
            Workload::Hot {
                count: 1,
                minimum: 255,
                maximum: 512,
            },
            Workload::Hot {
                count: 2,
                minimum: 512,
                maximum: 256,
            },
            Workload::Short {
                count: 0,
                sizes: vec![256],
            },
            Workload::Short {
                count: 1,
                sizes: vec![],
            },
            Workload::Short {
                count: 2,
                sizes: vec![255],
            },
            Workload::Short {
                count: 2,
                sizes: vec![512, 256],
            },
        ];
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("workload.yaml");
        let sidecar = path.with_extension("json");
        fs::write(&path, "original workload").unwrap();
        fs::write(&sidecar, "original fingerprint").unwrap();
        for workload in invalid {
            assert!(workload.validate().is_err(), "{workload:?}");
            assert!(workload.write(&path).is_err(), "{workload:?}");
            assert_eq!(fs::read_to_string(&path).unwrap(), "original workload");
            assert_eq!(
                fs::read_to_string(&sidecar).unwrap(),
                "original fingerprint"
            );
        }
    }

    #[test]
    fn singleton_and_constant_size_boundaries_are_supported() {
        let unique = prompts(&Workload::Unique {
            count: 1,
            bytes: 128,
            start: u64::MAX,
        });
        assert!(unique[0].starts_with("canonical-unique-session=18446744073709551615; "));
        let hot = prompts(&Workload::Hot {
            count: 1,
            minimum: 256,
            maximum: 512,
        });
        assert_eq!(hot[0].len(), 256);
        let workers = prompts(&Workload::SessionWorkers {
            workers: 1,
            session_tasks: vec![vec![69, 69]],
        });
        assert_eq!(workers[0].len(), 256);
        assert_eq!(workers[0], workers[1]);
    }
}
