# acRelay

[English](./README.md) · **한국어**

계획, 문서 또는 구현 결과에 다른 관점의 검토가 필요할 때 acRelay로 one-shot
red-team을 시작할 수 있습니다. 작업 중인 agent가 별도의 Claude Code 또는 Codex
CLI에 파일 하나나 지정한 파일 묶음을 검토하게 합니다. acRelay는 검토한 revision,
finding, 처리 결과와 owner의 최종 결정을 하나의 비공개 로컬 기록에 남깁니다.

변경을 작성하는 agent가 **driver**이고, 중요한 승인과 review 종료를 결정하는
사람이 **owner**입니다. acRelay는 코드를 merge하지 않으며, 실행 결과가 불확실한
review를 자동으로 다시 시도하거나 review를 스스로 종료하지 않습니다.

## acRelay를 만든 이유

acRelay는 실제 계획과 구현을 만들면서 Claude Code와 Codex 사이에 red-team 회차를
반복 운영한 경험에서 시작했습니다. 다른 agent가 놓친 부분을 찾아 계획과 결과를
보강하는 경우가 많았지만, 매 요청과 결과를 사람이 옮기는 작업까지 계속하고
싶지는 않았습니다.

Cross-agent review는 유용하지만 수동 relay는 금방 반복 작업이 됩니다. 매 회차마다
review 요청을 reviewer에게 복사하고, 결과를 다시 driver에게 복사해야 합니다.
3회차를 진행하면 최대 6번을 복사·붙여넣어야 하고, 어느 revision을 검토했는지와
어떤 finding이 남았는지도 사용자가 직접 관리해야 합니다.

| 수동 relay | acRelay 사용 |
| --- | --- |
| Agent 사이에서 매 요청과 결과를 복사 | 로컬 binary나 acRelay Skill로 relay 시작 |
| 회차, revision과 finding을 직접 관리 | 하나의 비공개 review 기록에 함께 보관 |
| 두 agent가 끝났는지 대화로 판단 | 종료 준비 요약을 확인하고 owner가 최종 결정 |

여기서 **one-shot**은 owner가 범위가 정해진 review objective 하나를 필요할 때
직접 시작한다는 뜻입니다. “prompt 한 번”이나 “회차 한 번”이라는 뜻은 아닙니다.
Objective별 formal round는 1–5회이며 기본값은 3회입니다. 제한된 회차는 끝없는
논쟁을 막고 reviewer token과 model 비용을 사용자가 통제하도록 돕습니다. 회차가
길어지면 피로, 반복 prompt와 context drift가 쌓여 새로운 검토 없이 승인 쪽으로
기울 수 있습니다. 5회차에 도달하면 현재 objective에 formal review 회차를 더
추가할 수 없습니다. 종료할지 새 objective를 의도적으로 시작할지는 owner가
결정합니다.

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

가장 자연스러운 구성은 Codex App, Claude Code CLI 또는 Codex CLI가 driver로서
acRelay Skill과 binary를 호출하고, Claude Code CLI나 Codex CLI가 reviewer를
맡는 방식입니다. 한 agent 생태계를 주로 쓰는 사용자도 지원하는 same-vendor 별도
CLI session을 사용할 수 있으며, acRelay는 두 context에 같은 맹점이 있을 수
있다는 caution을 기록합니다. Host-native subagent 결과를 직접 받는 기능은 이번
release에서 지원하지 않으며 다른 경로로 조용히 우회하지 않습니다.

[![Driver, 비공개 기록, owner와 reviewer service 사이에서 acRelay가 review를 전달하는 방식](./docs/assets/acrelay-architecture-trust.ko@2x.png)](./docs/assets/acrelay-architecture-trust.ko.svg)

## 현재 Alpha와 platform 지원 확대

첫 Alpha는 내려받을 수 있는 파일과 live review 검증 범위를 의도적으로 좁게
시작합니다.

- 내려받아 설치할 수 있는 binary: **macOS Apple Silicon (`darwin/arm64`)** 전용
- Reviewer: 실제로 검증한 reviewer version과 운영체제 조합의 Claude Code CLI와
  Codex CLI
- Release: signing과 notarization을 하지 않은 `v0.1.0-alpha.1`
- Review 방식: review마다 reviewer 1개, 정해진 회차 제한, 모든 finding에 대한
  driver의 처리 결정과 owner의 최종 종료 결정

Linux와 Windows는 다음 platform 지원 대상입니다. Core runtime은 이미 두
platform의 기록된 test lane을 통과했습니다. 다음 지원 확대 단계에서 platform별
Claude Code·Codex review를 검증하고, 필요한 patch는 그 근거를 검토한 뒤
release할 예정입니다. 그전까지 `v0.1.0-alpha.1`은 검증하지 않은 platform과
reviewer 조합에서 review를 보내기 전에 중단합니다. Intel Mac은 이번 release에서
내려받을 수 있는 artifact와 검증된 live-review 조합이 없습니다.

별도의 process나 vendor를 사용했다는 사실만으로 판단의 독립성이 증명되지는
않습니다.

## 설치

다음 명령을 사용하려면 `v0.1.0-alpha.1` tag와 release asset이 게시돼 있어야
합니다. 둘 중 하나라도 없다면 unpinned branch나 `latest` download로 바꾸지 말고
중단하세요.

### 한 줄로 binary 설치

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

PATH 설정, update, binary 제거, unsigned download 동작과 복구 절차는
[설치와 운영](./docs/OPERATIONS.ko.md)을 참고하세요.

## 자연어로 사용하려면 acRelay Skill 추가

Engine은 단독으로 완전하게 사용할 수 있지만, 일반 사용자가 낮은 수준의 command를
외울 필요는 없습니다. Optional
[Skillstead의 acRelay Skill](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay)을
설치하면 “Claude에게 이 계획을 red-team해 달라” 같은 자연어 요청을 같은
binary-enforced workflow로 바꿔 줍니다.

이 Skill은 Alpha preview이며 Claude Code와 Codex 별도 검증을 완료하는 중입니다.
Engine을 설치하거나 update하거나 대체하지 않습니다. Engine을 먼저 설치한 뒤,
preview를 평가하려면 Skill 폴더 전체를 복사하세요.

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

아직 공개하지 않은 preview를 의도적으로 평가할 때만 default branch를 사용하세요.
검증된 release tag가 생기면 해당 tag에 고정해서 설치해야 합니다. 프로젝트별 설치
경로, update, 제거와 현재 검증 상태는
[Skill 안내](https://github.com/kyungseo/skillstead/blob/main/skills/acrelay/README.ko.md)와
[Skillstead 설치 안내](https://github.com/kyungseo/skillstead/blob/main/docs/INSTALL.ko.md)를
참고하세요.

요청 예시:

> acRelay로 Claude가 이 계획을 red-team하게 해줘. Review 기록은 비공개로
> 보관하고, 최대 3회차 안에서 진행한 뒤 owner가 결정할 내용만 보여줘.

## 직접 CLI 사용: 첫 Review

공유·동기화 폴더와 repository 밖에 비공개 디렉터리를 만듭니다. acRelay는 review
이력의 기준이 되는 Markdown 파일을 **canonical record**라고 부릅니다.

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
