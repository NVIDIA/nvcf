// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use anyhow::{Context, Result, ensure};
use rustix::process::{Pid, Signal, WaitId, WaitIdOptions, kill_process_group, waitid};
use std::fmt;
use std::process::{ExitStatus, Output, Stdio};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::process::{Child, Command};

#[derive(Debug)]
pub struct Interrupted;

impl fmt::Display for Interrupted {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("interrupted")
    }
}

impl std::error::Error for Interrupted {}

pub async fn interrupted() -> Result<()> {
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        .context("listen for termination")?;
    tokio::select! {
        signal = tokio::signal::ctrl_c() => {
            signal.context("listen for interruption")?;
            Err(Interrupted.into())
        }
        signal = terminate.recv() => {
            signal.context("termination signal listener closed")?;
            Err(Interrupted.into())
        }
    }
}

pub struct Process {
    child: Child,
}

impl Process {
    pub fn spawn(mut command: Command) -> Result<Self> {
        command.process_group(0).kill_on_drop(true);
        let child = command.spawn().with_context(|| {
            format!("start {}", command.as_std().get_program().to_string_lossy())
        })?;
        Ok(Self { child })
    }

    pub fn try_wait(&mut self) -> Result<Option<ExitStatus>> {
        if let Some(group) = self.group_id()? {
            let status = waitid(
                WaitId::Pid(group),
                WaitIdOptions::EXITED | WaitIdOptions::NOHANG | WaitIdOptions::NOWAIT,
            )
            .context("observe child process status")?;
            if status.is_none() {
                return Ok(None);
            }
            // Keep the exited leader waitable until its group is stopped. Its
            // PID cannot be reused while we still own the unreaped child.
            self.kill_group()?;
        }
        self.child
            .try_wait()
            .context("reap completed child process")
    }

    pub async fn wait(&mut self) -> Result<ExitStatus> {
        let mut exits = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::child())
            .context("listen for child process exit")?;
        loop {
            if let Some(status) = self.try_wait()? {
                return Ok(status);
            }
            exits
                .recv()
                .await
                .context("child process exit listener closed")?;
        }
    }

    fn group_id(&self) -> Result<Option<Pid>> {
        self.child
            .id()
            .map(|id| {
                ensure!(id > 1, "refuse to signal an invalid child process group");
                Pid::from_raw(i32::try_from(id).context("child PID exceeds i32")?)
                    .context("child PID is zero")
            })
            .transpose()
    }

    fn kill_group(&self) -> Result<()> {
        if let Some(group) = self.group_id()? {
            match kill_process_group(group, Signal::KILL) {
                Ok(()) | Err(rustix::io::Errno::SRCH) => {}
                Err(error) => return Err(error).context("kill owned process group"),
            }
        }
        Ok(())
    }

    pub async fn stop(&mut self) -> Result<()> {
        self.kill_group()?;
        self.child
            .wait()
            .await
            .context("reap stopped child process")?;
        Ok(())
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        let _ = self.kill_group();
    }
}

pub async fn capture(
    mut command: Command,
    input: Option<&[u8]>,
    timeout: Duration,
) -> Result<Output> {
    let program = command
        .as_std()
        .get_program()
        .to_string_lossy()
        .into_owned();
    command
        .stdin(if input.is_some() {
            Stdio::piped()
        } else {
            Stdio::null()
        })
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut process = Process::spawn(command)?;
    let mut stdout = process.child.stdout.take().context("capture stdout")?;
    let mut stderr = process.child.stderr.take().context("capture stderr")?;
    let stdin = process.child.stdin.take();
    let mut output = Vec::new();
    let mut errors = Vec::new();
    let io = async {
        let write_input = async {
            if let Some(mut stdin) = stdin {
                match stdin
                    .write_all(input.context("missing command input")?)
                    .await
                {
                    Ok(()) => {}
                    Err(error) if error.kind() == std::io::ErrorKind::BrokenPipe => {}
                    Err(error) => return Err(error).context("write command input"),
                }
            }
            Ok::<_, anyhow::Error>(())
        };
        let (_, _, _, status) = tokio::try_join!(
            write_input,
            async {
                stdout
                    .read_to_end(&mut output)
                    .await
                    .context("read command stdout")
            },
            async {
                stderr
                    .read_to_end(&mut errors)
                    .await
                    .context("read command stderr")
            },
            process.wait(),
        )?;
        Ok::<_, anyhow::Error>(status)
    };
    let status = match tokio::time::timeout(timeout, io).await {
        Ok(status) => status,
        Err(error) => Err(error.into()),
    }
    .with_context(|| format!("capture {program}"));
    match status {
        Ok(status) => Ok(Output {
            status,
            stdout: output,
            stderr: errors,
        }),
        Err(error) => {
            process
                .stop()
                .await
                .with_context(|| format!("stop {program} after failure"))?;
            Err(error)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rustix::fd::OwnedFd;
    use rustix::process::{PidfdFlags, pidfd_open, pidfd_send_signal};
    use std::path::Path;

    struct Descendant {
        pid: u32,
        handle: OwnedFd,
    }

    impl Drop for Descendant {
        fn drop(&mut self) {
            let _ = pidfd_send_signal(&self.handle, Signal::KILL);
        }
    }

    async fn assert_stopped(pid: u32) {
        tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                match std::fs::read_to_string(format!("/proc/{pid}/stat")) {
                    Ok(stat) if stat.rsplit_once(") ").unwrap().1.starts_with('Z') => break,
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => break,
                    Ok(_) => tokio::time::sleep(Duration::from_millis(5)).await,
                    Err(error) => panic!("read fixture process {pid}: {error}"),
                }
            }
        })
        .await
        .expect("owned descendant remained alive");
    }

    async fn leader_with_descendant(path: &Path) -> (Process, Descendant) {
        let mut command = Command::new("sh");
        command
            .args([
                "-c",
                "sleep 30 >/dev/null 2>&1 & printf '%s' $! > \"$1\"; read token; exit 7",
                "--",
            ])
            .arg(path)
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        let mut process = Process::spawn(command).unwrap();
        let pid = tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                if let Ok(contents) = std::fs::read_to_string(path)
                    && let Ok(pid) = contents.parse::<u32>()
                {
                    break pid;
                }
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        // A pidfd makes test cleanup safe even if an assertion fails after the
        // fixture leader has exited and its process ID can be reused.
        let descendant = Descendant {
            pid,
            handle: pidfd_open(
                Pid::from_raw(i32::try_from(pid).unwrap()).unwrap(),
                PidfdFlags::empty(),
            )
            .unwrap(),
        };
        process
            .child
            .stdin
            .take()
            .unwrap()
            .write_all(b"finish\n")
            .await
            .unwrap();
        (process, descendant)
    }

    #[tokio::test]
    async fn captures_both_streams_and_preserves_exit_status() {
        let mut command = Command::new("sh");
        command.args(["-c", "cat; printf diagnostic >&2; exit 7"]);
        let output = capture(command, Some(b"input"), Duration::from_secs(5))
            .await
            .unwrap();
        assert_eq!(output.status.code(), Some(7));
        assert_eq!(output.stdout, b"input");
        assert_eq!(output.stderr, b"diagnostic");
    }

    #[tokio::test]
    async fn a_timed_out_child_is_stopped_and_reaped() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("pid");
        let mut command = Command::new("sh");
        command.args(["-c", "printf '%s' $$ > \"$1\"; exec sleep 30", "--"]);
        command.arg(&path);
        let error = capture(command, None, Duration::from_millis(200))
            .await
            .unwrap_err();
        assert!(error.is::<tokio::time::error::Elapsed>());
        let pid = std::fs::read_to_string(path).unwrap();
        assert!(!std::path::Path::new(&format!("/proc/{pid}")).exists());
    }

    #[tokio::test]
    async fn early_stdin_closure_preserves_large_outputs_and_exit_status() {
        let mut command = Command::new("sh");
        command.args([
            "-c",
            "head -c 131072 /dev/zero; head -c 131072 /dev/zero >&2; printf diagnostic >&2; exit 7",
        ]);
        let input = vec![b'x'; 1024 * 1024];
        let output = capture(command, Some(&input), Duration::from_secs(5))
            .await
            .unwrap();
        assert_eq!(output.status.code(), Some(7));
        assert_eq!(output.stdout.len(), 131072);
        assert!(output.stdout.iter().all(|byte| *byte == 0));
        assert_eq!(output.stderr.len(), 131072 + b"diagnostic".len());
        assert!(output.stderr[..131072].iter().all(|byte| *byte == 0));
        assert_eq!(&output.stderr[131072..], b"diagnostic");
    }

    #[tokio::test]
    async fn exit_observation_cleans_the_group_before_reaping_the_leader() {
        for poll in [false, true] {
            let directory = tempfile::tempdir().unwrap();
            let (mut process, descendant) =
                leader_with_descendant(&directory.path().join("pid")).await;
            let leader = process.child.id().unwrap();
            let status = if poll {
                tokio::time::timeout(Duration::from_secs(2), async {
                    loop {
                        if let Some(status) = process.try_wait().unwrap() {
                            break status;
                        }
                        tokio::time::sleep(Duration::from_millis(5)).await;
                    }
                })
                .await
                .unwrap()
            } else {
                tokio::time::timeout(Duration::from_secs(2), process.wait())
                    .await
                    .unwrap()
                    .unwrap()
            };
            assert_eq!(status.code(), Some(7));
            assert_eq!(process.try_wait().unwrap(), Some(status));
            process.stop().await.unwrap();
            assert!(!Path::new(&format!("/proc/{leader}")).exists());
            assert_stopped(descendant.pid).await;
        }
    }

    #[tokio::test]
    async fn capture_stops_inherited_pipe_holders_when_the_leader_exits() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("pid");
        let mut command = Command::new("sh");
        command
            .args(["-c", "sleep 30 & printf '%s' $! > \"$1\"; exit 7", "--"])
            .arg(&path);
        let output = capture(command, None, Duration::from_secs(2))
            .await
            .unwrap();
        assert_eq!(output.status.code(), Some(7));
        let descendant = std::fs::read_to_string(path).unwrap().parse().unwrap();
        assert_stopped(descendant).await;
    }

    #[tokio::test]
    async fn dropping_an_unobserved_leader_stops_its_group_and_reaps_it() {
        let directory = tempfile::tempdir().unwrap();
        let (process, descendant) = leader_with_descendant(&directory.path().join("pid")).await;
        let leader = process.child.id().unwrap();
        drop(process);
        assert_stopped(descendant.pid).await;
        tokio::time::timeout(Duration::from_secs(2), async {
            while Path::new(&format!("/proc/{leader}")).exists() {
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .expect("dropped leader was not reaped");
    }
}
