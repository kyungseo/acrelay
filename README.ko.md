# acRelay

[English](./README.md) · **한국어**

계획이나 구현 결과를 다른 coding agent와 함께 검토하면 방향을 더 정교하게
다듬고, 숨은 결함을 찾고, 최종 결과의 품질을 높일 수 있습니다. acRelay는 필요한
순간에 이런 review 회차를 쉽게 시작하도록 돕습니다. 작업 중인 agent가 별도의
Claude Code 또는 Codex CLI에 파일 하나나 지정한 파일 묶음을 검토하게 합니다.

검토한 revision, finding, 처리 결과와 owner의 최종 결정은 공유·동기화 폴더가
아닌 사용자 컴퓨터의 기록 하나에 함께 남습니다.

변경을 작성하는 agent가 **driver**이고, 최종 승인과 review 종료를 결정하는
사람이 **owner**입니다. acRelay는 review를 정리하고 기록하지만 owner를 대신해
결정하지 않습니다.

## 시작 전 준비

이 preview는 Codex App, Codex CLI 또는 Claude Code로 파일 작업을 하는 사용자를
대상으로 합니다. Review는 Claude Code CLI나 Codex CLI를 통해 실행하므로 둘 중
하나는 미리 설치하고 로그인해 정상 실행되는 상태여야 합니다. Codex App은
driver가 될 수 있지만 reviewer는 CLI에서 실행됩니다.

여기서 **App**은 데스크톱 화면, **CLI**는 Terminal에서 실행하는 command를
뜻합니다. acRelay Skill과 engine은 reviewer 도구를 대신 설치하거나 로그인하지
않습니다.

## acRelay를 만든 이유

acRelay는 한 도구에서 계획이나 구현을 작성한 뒤, owner가 결정하기 전에 다른
도구에 반대 관점의 검토를 요청하던 작업 방식에서 시작했습니다. Review 자체는
유용했지만, 요청과 결과를 옮기는 과정이 불편했습니다.

매 회차마다 review 요청을 reviewer에게 복사하고, 결과를 다시 driver에게
복사해야 했습니다. 3회차를 진행하면 최대 6번을 복사·붙여넣어야 하고, 어느
revision을 검토했는지와 어떤 finding이 남았는지도 사용자가 직접 관리해야
했습니다.

| 수동 relay | acRelay 사용 |
| --- | --- |
| Agent 사이에서 매 요청과 결과를 복사 | 로컬 binary나 acRelay Skill로 relay 시작 |
| 회차, revision과 finding을 직접 관리 | 사용자 컴퓨터의 review 기록 하나에 함께 보관 |
| 두 agent가 끝났는지 대화로 판단 | 종료 준비 요약을 확인하고 owner가 최종 결정 |

Review objective별 formal round는 1–5회이며 기본값은 3회입니다. 제한된 회차는
끝없는 논쟁을 막고 reviewer token과 model 비용을 사용자가 통제하도록 돕습니다.
회차가 길어지면 피로, 반복 prompt와 context drift가 쌓여 새로운 검토 없이 승인
쪽으로 기울 수 있습니다. 5회차에 도달하면 종료할지 새 objective를 의도적으로
시작할지 owner가 결정합니다.

사용하는 방법은 단순하지만 내부 계약까지 단순한 것은 아닙니다. Reviewer가
“괜찮다”고 답했다는 이유만으로 끝내지 않고, 검토한 revision, finding, 응답,
복구 상태, 회차 제한과 owner 권한을 서로 구분해 관리합니다.

## 실행 파일 하나, acRelay daemon 없음

acRelay는 실행 파일 하나로 배포합니다. 자체 daemon, server, database, queue
또는 백그라운드 network service를 실행하지 않습니다. Review를 요청하면 별도로
설치하고 인증한 Claude Code 또는 Codex CLI를 그때 시작하고, 응답 형식을 확인해
결과를 기록합니다.

Reviewer CLI는 provider network를 사용하고 model token을 소비할 수 있습니다.
“로컬 기록”은 acRelay의 review 이력이 로컬에 남는다는 뜻이지 reviewer model이
로컬에서 실행된다는 뜻은 아닙니다.

## Driver와 reviewer 선택

| 사용 방식 | Driver | Reviewer |
| --- | --- | --- |
| Codex App에서 작업 | Codex App | Claude Code CLI 또는 Codex CLI |
| Claude Code에서 작업 | Claude Code CLI | Codex CLI 또는 별도의 Claude Code CLI session |
| Claude Code만 사용 | Claude Code CLI | 별도의 Claude Code CLI session |
| Codex만 사용 | Codex CLI | 별도의 Codex CLI session |

다른 도구를 reviewer로 쓰면 driver가 놓친 가정을 다른 관점에서 검토할 수
있습니다. Codex나 Claude Code 중 하나만 사용하더라도 같은 도구의 별도 CLI
session을 reviewer로 둘 수 있지만, driver와 reviewer가 같은 맹점을 공유할 수
있습니다. 이 preview는 CLI reviewer를 사용하며, driver 도구가 만든 내장
subagent 결과를 직접 받지는 않습니다.

[![Driver, 사용자 컴퓨터의 기록, owner와 reviewer service 사이에서 acRelay가 review를 전달하는 방식](./docs/assets/acrelay-architecture-trust.ko@2x.png)](./docs/assets/acrelay-architecture-trust.ko.svg)

## Public Validation Preview와 platform 지원 확대

`v0.1.0-alpha.1`은 **Public Validation Preview**입니다. 아직
**Experimental** 단계이고 더 넓은 검증은 **Validation pending**이며, 일반적인
`Supported` 상태를 주장하지 않습니다. 내려받을 수 있는 파일과 live review 검증
범위는 의도적으로 좁게 시작합니다.

- 내려받아 설치할 수 있는 binary: **macOS Apple Silicon (`darwin/arm64`)** 전용
- Reviewer: 실제로 검증한 reviewer version과 운영체제 조합의 Claude Code CLI와
  Codex CLI
- Release: Developer ID signing과 notarization을 하지 않은 `v0.1.0-alpha.1`
- Review 방식: review마다 reviewer 1개, 정해진 회차 제한, 모든 finding에 대한
  driver의 처리 결정과 owner의 최종 종료 결정

Linux와 Windows는 다음 platform 지원 대상입니다. Core runtime은 이미 두
platform의 기록된 test lane을 통과했습니다. 다음 지원 확대 단계에서 platform별
Claude Code·Codex review를 검증하고, 필요한 patch는 그 근거를 검토한 뒤
release할 예정입니다. 그전까지 `v0.1.0-alpha.1`은 검증하지 않은 platform과
reviewer 조합에서 review를 보내기 전에 중단합니다. Intel Mac은 이번 release에서
내려받을 수 있는 artifact와 검증된 live-review 조합이 없습니다.

## 설치

정확한 `v0.1.0-alpha.1` preview를 설치합니다. Installer는 unpinned branch나
`latest` download로 바꾸지 않습니다.

### 한 줄로 binary 설치

macOS에서 Terminal을 열고 `uname -m`을 실행하세요. 결과가 `arm64`일 때만 아래
미리 build한 binary installer를 사용합니다.

Installer는 정확한 tag에 고정돼 있으며, 내려받은 binary archive를 실행하기 전에
release checksum과 대조합니다.

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh | bash
```

Script를 `bash`로 바로 보내면 편리하지만 실행 전에 installer 내용을 읽을 수는
없습니다. 먼저 내용을 확인하려면 아래 경로를 사용하세요.

### 내용을 먼저 확인하고 binary 설치

Installer는 release 하나에 고정되며 `latest`를 조회하지 않습니다. 실행 전에
내용을 검토하세요.

```sh
curl -fLO https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh
less install.sh
bash install.sh
```

기본 설치 경로는 `~/.local/bin`이며 `sudo`를 사용하지 않습니다. Release
archive의 checksum을 확인하기 전에는 archive 안의 binary를 실행하지 않고,
설치된 version이 다르면 `--replace` 없이는 교체하지 않습니다.

### Go install

```sh
go install github.com/kyungseo/acrelay/cmd/acrelay@v0.1.0-alpha.1
```

Source install에는 [`go.mod`](./go.mod)에 선언된 Go toolchain이 필요합니다.
사용자의 `GOTOOLCHAIN` 설정에 따라 Go가 해당 toolchain을 내려받을 수 있습니다.

어느 경로로 설치했든 다음 명령으로 확인합니다.

```sh
acrelay version
```

PATH 설정, update, binary 제거, macOS signing·Gatekeeper 동작과 복구 절차는
[설치와 운영](./docs/OPERATIONS.ko.md)을 참고하세요.

## 자연어로 사용하려면 acRelay Skill 추가

Engine은 단독으로 완전하게 사용할 수 있지만, 일반 사용자가 낮은 수준의 command를
외울 필요는 없습니다. Optional
[Skillstead의 acRelay Skill](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay)을
설치하면 “Claude에게 이 계획을 red-team해 달라” 같은 자연어 요청을 같은
workflow로 바꿉니다. Skill은 요청을 acRelay 단계로 옮기고, engine은 파일 확인,
reviewer 실행과 review 기록을 담당합니다. Skill만으로는 review를 실행할 수
없으므로 둘 다 설치합니다.

이 Skill은 **Public Validation Preview**입니다. 아직 **Experimental** 단계이고,
더 넓은 환경의 검증은 **Validation pending**이며, 일반적인 `Supported` 상태를
주장하지 않습니다. Engine을 설치하거나 update하거나 대체하지 않습니다. Engine을
먼저 설치한 뒤 Skill 폴더 전체를 복사하세요.

### Claude Code

```sh
git clone --depth 1 https://github.com/kyungseo/skillstead.git /tmp/skillstead
mkdir -p "$HOME/.claude/skills"
cp -R /tmp/skillstead/skills/acrelay "$HOME/.claude/skills/"
```

### Codex

```sh
git clone --depth 1 https://github.com/kyungseo/skillstead.git /tmp/skillstead
mkdir -p "$HOME/.agents/skills"
cp -R /tmp/skillstead/skills/acrelay "$HOME/.agents/skills/"
```

이 preview는 Skillstead의 default branch에서 설치하며 `v0.8.0` Skillstead tag에는
포함되지 않습니다. 별도로 검증한 release tag가 생기면 이 경로를 대체할
예정입니다. 프로젝트별 설치 경로, update, 제거와 현재 검증 상태는
[Skill 안내](https://github.com/kyungseo/skillstead/blob/main/skills/acrelay/README.ko.md)와
[Skillstead 설치 안내](https://github.com/kyungseo/skillstead/blob/main/docs/INSTALL.ko.md)를
참고하세요.

요청 예시:

> acRelay로 Claude에게 이 계획을 비판적으로 검토해 달라고 해줘. 마지막에 내가
> 결정해야 할 내용만 정리해줘.

별도로 지정하지 않으면 최대 3회차로 진행합니다. 필요하면 1~5회 안에서 원하는
제한을 요청할 수 있습니다.

## 직접 CLI 사용: 첫 Review

공유·동기화하지 않고 repository에도 포함하지 않을 사용자 컴퓨터의 디렉터리를
만듭니다. acRelay는 review 이력의 기준이 되는 Markdown 파일을
**canonical record**라고 부릅니다.

```sh
mkdir -p "$HOME/.acrelay/reviews"
```

Review 하나를 시작합니다.

```sh
acrelay init \
  -canonical "$HOME/.acrelay/reviews/example.md" \
  -question "Is this change ready to ship?" \
  -target ./README.md \
  -approval-actor owner \
  -ack-vendor-egress \
  -execution-surface external-cli \
  -driver-vendor codex \
  -context-relation separate
```

`-ack-vendor-egress`는 검토할 파일, 해석된 파일 경로와 관련 metadata가 선택한
reviewer service로 전달될 수 있음을 확인하는 옵션입니다. Review 기록은 로컬에
남지만 Claude Code나 Codex가 provider로 데이터를 보낼 수 있습니다.

첫 reviewer 회차를 실행합니다.

```sh
acrelay review \
  -canonical "$HOME/.acrelay/reviews/example.md" \
  -reviewer claude \
  -prompt "Review the target against the objective. Return examined evidence and structured findings."
```

이후 driver는 각 finding을 수용할지, 수정할지, 반론할지, owner의 결정이 필요한지
선택하고 이유를 기록합니다. Approval request에는 owner가 응답하며, 마지막
`close` 명령도 owner가 실행합니다. 아무것도 변경하지 않고 종료 준비 상태만
확인하려면 다음 명령을 사용합니다.

```sh
acrelay briefing \
  -canonical "$HOME/.acrelay/reviews/example.md"
```

`briefing`은 현재 기록을 요약할 뿐입니다. Approval이 아니며 review 상태를
변경하지 않습니다.

## acRelay가 기록하는 것

- Review 대상으로 선택한 정확한 파일 목록과 중요한 동작 전에 다시 확인하는 revision
- Reviewer가 확인했다고 제출한 발췌문과 structured finding
- 중단된 reviewer 실행을 복구하기 위한 journal. 결과가 불확실하면 `UNKNOWN`으로
  기록하고 자동으로 다시 시도하지 않음
- 선택한 reviewer와 formal review 회차 제한
- 모든 finding에 대한 driver의 처리 결정과 이유
- Owner에게 요청한 승인과 owner의 응답
- Owner의 `close` 명령과 분리된 read-only 종료 준비 요약

이 기록은 reviewer가 모든 내용을 이해했는지, review가 완전하거나 정확했는지,
독립적인 판단이었는지를 증명하지 않습니다. acRelay가 관측한 사실과 driver,
reviewer 또는 owner가 선언한 내용만 보여줍니다.

## Private state

Canonical record에는 reviewer output, prompt, 파일 경로와 실행 정보가 포함될 수
있습니다. 비공개로 보관하세요. 이번 release는 공유용으로 민감 정보를 제거한
사본을 만들 수 없습니다.

`~/.acrelay`에는 review를 이어갈 때 사용하는 reviewer session 식별자와 비공개
working directory가 저장됩니다. Binary를 제거해도 이 정보는 삭제되지 않습니다.
보존된 acRelay 정보를 삭제하려면 정확한 canonical record와 session reference를
지정해 `acrelay cleanup`을 실행해야 합니다. Claude Code, Codex 또는 provider가
소유한 session·설정 정보는 acRelay가 삭제할 수 없으며 삭제됐다고 확인할 수도
없습니다.

## 문서

- [구조와 신뢰 경계](./docs/ARCHITECTURE.ko.md)
- [동작 및 CLI reference](./docs/REFERENCE.ko.md)
- [설치, update, 제거와 release 복구](./docs/OPERATIONS.ko.md)
- [Release 기록](./CHANGELOG.ko.md)
- [자연어 acRelay Skill](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay)
- [Skillstead 설치 안내](https://github.com/kyungseo/skillstead/blob/main/docs/INSTALL.ko.md)

Skill은 더 쉬운 진입점이며, engine 동작, evidence, privacy, 복구와 platform
지원의 기준 문서는 이 repository에 있습니다.

## License

[Apache-2.0](./LICENSE)을 적용합니다. 전체 조건과 warranty 제한은 license
원문을 확인하세요.
