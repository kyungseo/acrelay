# acRelay 설치와 운영

[English](./OPERATIONS.md) · **한국어**

이 문서는 `v0.1.0-alpha.4` **Public Validation Preview**의 설치, update, 제거와
복구 방법을 설명합니다. 아직 **Experimental** 단계이며, 더 넓은 환경의 검증은
**Validation pending**입니다.

미리 build해 제공하는 binary는 macOS Apple Silicon(`darwin/arm64`)용 하나입니다.
다음 platform 지원 대상은 Windows입니다. Windows core runtime lane은 이미
검증했으며, 다음 단계에서 Claude Code·Codex review를 검증합니다. 필요한 patch는
그 근거를 검토한 뒤 release합니다. Linux core runtime CI는 source test matrix에
유지하지만, 이번 preview에는 Linux artifact와 live-review 지원이 없습니다.
Intel Mac도 이번 release에서 미리 build한 binary와 검증된 live-review 조합이
없습니다.

## 설치 경로 선택

| 방법 | 적합한 경우 | 알아둘 제한 |
| --- | --- | --- |
| 한 줄 installer | 공개된 engine과 optional Skill을 가장 빠르게 설치 | Archive checksum은 확인하지만 installer를 실행 전에 읽지는 않음 |
| Tag에 고정된 installer | 공개된 engine과 optional Skill을 사용자 계정에 설치 | Developer ID signing이나 notarization을 하지 않음. Checksum은 파일이 바뀌었는지 확인하지만 누가 게시했는지는 증명하지 않음 |
| 내려받아 읽은 뒤 실행 | Installer 내용을 직접 확인하고 실행 | 같은 binary를 설치하고 같은 checksum을 사용 |
| 고정 version의 `go install` | 기존 Go toolchain으로 source에서 build | 로컬 설정에 따라 필요한 toolchain과 module data를 내려받을 수 있음 |

이 repository가 engine과 optional
[acRelay Skill](../skills/acrelay/README.ko.md)의 canonical source입니다.
`--skill-host codex`, `claude` 또는 `both`를 지정하면 같은 검증 archive에 담긴
exact Skill을 함께 설치합니다. Engine만 설치하려면 option을 생략합니다.
Skill도 Public Validation Preview에 포함되며, 아직 Experimental이고 Validation
pending이며 일반적인 `Supported` 상태를 주장하지 않습니다.

## 한 줄 installer

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.4/scripts/install.sh |
  bash -s -- --skill-host both
```

Script는 `v0.1.0-alpha.4`에 고정돼 있고, 내려받은 binary archive를 실행하기 전에
release checksum과 대조합니다. Script를 `bash`로 바로 보내면 installer 자체를
실행 전에 읽을 수는 없습니다. 이 차이가 중요하면 아래의 tag 고정·사전 확인
경로를 사용하세요.

## Tag에 고정하고 내용을 먼저 확인하는 installer

정확한 release tag에서 script를 내려받아 내용을 확인한 뒤 실행합니다.

```sh
curl -fLO https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.4/scripts/install.sh
less install.sh
bash install.sh
```

Script가 설치할 version은 `v0.1.0-alpha.4`로 고정되어 있습니다. `latest`를
조회하거나 임의의 version을 입력받지 않습니다. 다음 파일을 내려받습니다.

```text
acrelay_0.1.0-alpha.4_darwin_arm64.tar.gz
acrelay_0.1.0-alpha.4_checksums.txt
```

Archive의 SHA-256 값이 공개된 checksum 항목과 정확히 일치하는지 확인한 뒤에만
압축을 풀고 binary를 실행합니다. Archive에는 최상위 directory 하나가 있습니다.

```text
acrelay_0.1.0-alpha.4_darwin_arm64/
├── acrelay
├── LICENSE
├── README.md
└── skills/
    └── acrelay/
```

기본 설치 경로는 `~/.local/bin/acrelay`입니다. 사용자가 소유한 다른 directory에
설치하려면 다음과 같이 지정합니다.

```sh
bash install.sh --bin-dir "$HOME/bin"
```

Installer는 `sudo`를 사용하지 않습니다. 설치 directory가 `PATH`에 없다면
추가해야 할 정확한 경로를 알려줍니다.

### 기존 설치가 있는 경우

- 같은 version: 교체하지 않고 성공으로 종료합니다.
- 다른 version 또는 version을 확인할 수 없음: 설치를 바꾸지 않고 중단하며 현재
  version과 설치하려던 version을 표시합니다.
- 같은 Skill: 바꾸지 않습니다. 로컬 Skill 내용이 다르면 engine이나 요청한 다른
  host를 바꾸기 전에 중단합니다.
- 명시적인 교체: engine 및/또는 Skill 교체 의도를 확인한 뒤 `--replace`로 다시
  실행합니다.
- 기존 경로의 파일을 실행할 수 없음: 덮어쓰지 않고 실패합니다.

`--replace`는 downgrade 가능성을 포함해 교체를 명시적으로 허용합니다.
Installer가 semantic version을 비교해 어느 쪽이 최신인지 판단하지는 않습니다.

## 고정 version으로 Go install

```sh
go install github.com/kyungseo/acrelay/cmd/acrelay@v0.1.0-alpha.4
```

Module의 `go` directive에 필요한 toolchain version이 선언되어 있습니다. Go의
automatic toolchain selection이 켜져 있으면 `go` command가 해당 toolchain을
내려받을 수 있습니다. 이 network 동작을 허용할 수 없는 환경에서는
`go env GOTOOLCHAIN`을 먼저 확인하세요.

이 방법은 사용자가 설정한 Go module proxy와 checksum 정책으로 module 내용을
확인하며 GitHub Release archive는 사용하지 않습니다.

다른 platform에서 build에 성공했다는 사실만으로 그 platform의 Claude Code 또는
Codex reviewer 경로가 활성화되지는 않습니다. Platform 근거는 계속
GOOS/GOARCH별로 분리합니다. 검증된 platform에서도 reviewer CLI는 문서화한 최소
version 이상이어야 하며, 제한 실행에 필요한 option을 모두 노출해야 합니다.

## 설치 확인

```sh
acrelay version
acrelay version --short
```

이번 release의 short output은 다음과 같아야 합니다.

```text
v0.1.0-alpha.4
```

Release file이 어떤 source와 환경에서 만들어졌는지 확인할 수 있도록 다음 파일도
게시합니다.

```text
acrelay_0.1.0-alpha.4_provenance.json
```

이 파일에는 source commit, build 환경, Go version, target `GOOS/GOARCH`, CGO
설정과 해당되는 경우 GitHub run identity가 기록됩니다. 실제 게시 후보 release
file은 이미 승인된 정확한 tag를 대상으로 수동 실행한 GitHub Actions workflow만
만듭니다. Local packaging script가 만든 파일은 test fixture이며 게시용 source가
아닙니다.

## macOS signing과 Gatekeeper의 한계

첫 release는 Developer ID signing이나 notarization을 하지 않습니다. Browser나
Finder로 내려받은 파일은 Gatekeeper warning이 나타나거나 실행이 차단될 수
있습니다. Terminal에서 한 번 성공했다는 사실만으로 모든 환경에서 warning 없이
실행된다고 보장하지 않습니다.

현재 근거는 release validation에서 직접 확인한 설치 경로에 한정되며, 일반적인
`Supported` 상태를 뜻하지 않습니다. 실행이 막히면 Gatekeeper message와 file
attribute를 기록하고 중단하세요. Gatekeeper를 전역으로 끄거나 dialog의 정확한
문구를 추정하면 안 됩니다.

## 업데이트

Installer는 release마다 version이 고정되어 있습니다. Engine과 설치한 Skill을
함께 update하려면 새 release tag의 installer를 내려받아 내용을 확인하고, 현재
version과 설치할 version을 확인한 뒤 같은 `--skill-host`와 `--replace`를 함께
사용합니다. `--skill-host`를 생략하면 engine만 update합니다.

Update는 `~/.acrelay`, reviewer session 식별자, 비공개 working directory,
canonical review record, 복구 journal 또는 quarantine data를 옮기거나 삭제하지
않습니다.

## Binary 제거

먼저 설치된 실행 파일의 경로를 확인합니다.

```sh
command -v acrelay
```

확인한 binary만 제거합니다. 기본 installer 경로라면 다음과 같습니다.

```sh
rm "$HOME/.local/bin/acrelay"
```

Binary를 제거해도 비공개 review 정보는 그대로 보존됩니다. Uninstall 과정에서
`~/.acrelay`를 삭제하지 마세요.

공식 Skill을 global로 설치했다면 Codex는
`$HOME/.agents/skills/acrelay`, Claude Code는
`$HOME/.claude/skills/acrelay` exact directory만 제거합니다. 로컬 수정이 있을
수 있으므로 먼저 내용을 확인하세요.

완료된 review의 acRelay session data도 정리하려면 binary를 제거하기 전에 cleanup
계획을 확인하세요. 정확한 canonical record와 session reference를 지정해야 합니다.

```sh
acrelay cleanup -canonical /private/path/review.md -ref sref-... -mode list
acrelay cleanup -canonical /private/path/review.md -ref sref-... -mode dry-run
```

Review가 cleanup을 허용하는 종료 상태에 도달하고, 관련 review가 이 session을
다시 사용하지 않는다고 owner가 기록해야만 `-mode apply`를 실행할 수 있습니다.
Raw canonical record는 owner가 계속 보관합니다. acRelay는 reviewer vendor가
소유한 session·설정 정보를 삭제하지 않으며 삭제했다고 표시하지도 않습니다.

## Release 파일을 만드는 방법

Maintainer용 release workflow는 다음 순서를 따릅니다.

1. Exact approved tag `v0.1.0-alpha.4`을 요구합니다.
2. Tag가 checkout commit을 가리키는지 확인합니다.
3. `darwin/arm64` builder와 정확한 Go toolchain을 확인합니다.
4. Deterministic/race test, vet, build와 module verification을 실행합니다.
5. `scripts/package-release.sh`를 실행합니다.
6. `scripts/test-release.sh`로 실제 package → checksum → installer → version
   chain을 검증합니다.
7. 결과를 보존 기간이 짧은 Actions artifact bundle로 저장합니다.

Workflow는 tag를 만들거나 push하지 않으며 repository visibility·settings를
바꾸거나 GitHub Release를 publish하지 않습니다. 각 작업은 owner가 따로
승인해야 합니다.

Archive가 byte 단위로 언제나 같다는 재현성은 주장하지 않습니다. 반복 가능한
build step과 build 출처를 기록하지만, runner image나 실행 시점이 다를 때도
archive hash가 같다고 약속하지 않습니다.

## 잘못된 release를 복구하는 방법

먼저 배포를 중단하고 무엇이 게시됐는지 분류합니다.

| Surface | 복구 action | Boundary |
| --- | --- | --- |
| GitHub Release asset | 영향받은 asset 또는 release를 내리고 corrected version/checksum notice 게시 | 이미 내려받은 파일은 회수할 수 없음 |
| Go module version | `retract` directive가 포함된 newer module 게시 | Published version과 checksum record가 남을 수 있음 |
| Broken Alpha version | 이를 supersede하는 patch/prerelease 게시 | Existing tag를 변경하지 않음 |
| Installer와 checksum이 맞지 않음 | 설치를 중단하고 새 version으로 교체하며 영향받은 파일을 정확히 공지 | 이미 게시한 version의 파일을 조용히 덮어쓰지 않음 |

Published tag를 force-update하거나 재사용하지 않습니다. Repository를 다시
private로 바꿔도 clone, cache, downloaded asset, module mirror 또는 checksum
record를 회수할 수 없습니다.

## Source에서 build

개발 환경에서는 다음 명령을 사용합니다.

```sh
go build -o acrelay ./cmd/acrelay
go vet ./...
go test ./... -race -count=1
go mod verify
```

이 test suite는 reviewer 인증, model/API 비용 또는 실제 reviewer 실행을
사용하지 않습니다. 실제 reviewer를 사용하는 smoke test는 owner가 별도로
승인해야 합니다.
