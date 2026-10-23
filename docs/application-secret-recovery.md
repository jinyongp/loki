# 애플리케이션 값 이관과 복구

이 문서는 devtools 0.33.0 / CLI protocol 12를 고정한 현재 후보의 절차다.
값은 전용 secrets 서비스가 관리한다. coordination의 task/workstream root는
값 이관·복원 대상에 포함하지 않는다. 운영 적용 전에 같은 후보의 필수
amd64·arm64 acceptance 결과를 확인한다.

## 준비

명령은 설치를 소유한 호스트 관리자 권한으로 실행한다. 아래 `STATE`,
`PROJECT`, `BACKUP_ID`, `RECIPIENT`는 해당 설치와 백업의 공개 메타데이터다.
`STATE`는 workspace 밖의 기존 private host state-root 절대 경로다.
복구 identity는 그 아래 관리자 소유의 일반 파일로, mode 0600이고 다른
hard link가 없어야 한다. passphrase와 identity 내용을 argv에 넣지 않는다.
예시의 private 입력 파일도 관리자만 읽을 수 있어야 한다.

기존 active protected Jobs는 기본적으로 maintenance admission을 차단한다.
실행 중인 작업을 중단하기로 결정한 경우에만 prepare 시
`--interrupt-active-jobs`를 지정한다. 이 선택은 계획에 고정된다.
복원은 외부 시스템에서 이미 일어난 효과를 취소하지 않는다.

## 원본을 보존하는 이관

`loki host secret-migration`은 관리형 full 설치의 `--manager-root`,
`loki host secret-migration compose`는 `--state-root`와 `--project`,
`loki host secret-migration native`는 native 설치의 원래 owner를 사용한다.
각 모드의 `prepare --help`로 경로와 입력을 확인한다.

prepare에는 명시적 mapping/resources 문서, X25519 공개 recipient,
아직 사용하지 않은 private source-backup 경로가 필요하다. 공개 변수로
분류하거나 이름을 변경하려면 mapping에 직접 지정한다. platform credential
프로필은 앱으로 가져오지 않는다. 잘못된 이름·충돌·크기 초과·source revision
변경은 cutover 전에 거절된다.

prepare 결과를 mode 0600의 계획 파일에 저장하고 검토한 뒤
`apply --plan ABSOLUTE_PLAN --approve`를 실행한다. 새 candidate는 자동으로
초기화하거나 unlock하지 않는다. `status`에서 같은 operation과 protection
상태를 확인하고 `recover --protection init --stdin` 또는 기존 candidate에
`recover --protection unlock --stdin`으로 private 입력을 전달한다.
중단 뒤에는 새 계획을 만들지 말고 원래 operation의 `recover`를 사용한다.
cutover 전 취소는 `rollback --approve`를 사용한다. 원본과 source archive는
기본 삭제하지 않는다.

## Compose 앱 백업과 같은 호스트의 복원

먼저 serving vault를 관리자 경로로 unlock한다. 원래 host operation이
maintenance를 잡은 동안 암호화된 logical app export와 source revision을
확보하고, 이어서 기존 offline snapshot을 만든다. 최초 백업에서는 공개
암호화 recipient를 지정한다.

```sh
umask 077
loki host backup --state-root "$STATE" \
  --application-backup-recipient "$RECIPIENT"
loki host application-restore compose prepare \
  --state-root "$STATE" --project "$PROJECT" --backup-id "$BACKUP_ID" \
  > "$STATE/restore-plan.json"
loki host application-restore compose apply \
  --state-root "$STATE" --project "$PROJECT" \
  --plan "$STATE/restore-plan.json" --approve
```

검토할 계획에는 현재/대상 프로그램과 immutable image, 암호화 app backup,
policy와 새 generation이 함께 들어 있다. 호환되는 이전 프로그램과 그
백업 데이터를 한 transaction으로 선택하는 paired rollback도 이 경로다.
프로그램 파일만 과거 버전으로 바꾸지 않는다.

apply가 수동 protection을 기다리면 다음처럼 같은 operation을 재개한다.
아래는 새 candidate의 초기화 예시다. candidate가 이미 초기화되어 locked이면
`init` 대신 `unlock`을 사용한다.

```sh
loki host application-restore compose recover \
  --state-root "$STATE" --project "$PROJECT" \
  --identity-file "$STATE/operator-age-identity" \
  --protection init --stdin < /private/new-candidate-passphrase
loki host application-restore compose status \
  --state-root "$STATE" --project "$PROJECT"
```

완료는 같은 operation의 `succeeded`와 maintenance release를 확인한다.
serving 서비스의 cold restart 뒤에는 다시 관리자 unlock이 필요하다.
원래 host/launcher journal과 더 최신 spent request 이력은 보존하고 과거
tombstone을 병합한다. 과거 session·execution snapshot·approval·pattern은
다시 실행할 권한으로 복원하지 않는다. cutover 전 중단은 같은 명령군의
`rollback --approve`로 처리한다.

구 physical restore 명령은 이 절차를 대신할 수 없다. 앱 상태나 protected
요청 이력을 통째로 되돌릴 수 있는 복원은 side effect 전에 거절한다.
현재 공개 logical app restore driver는 Compose용이다. native와 관리형 full의
이관 명령을 해당 모드의 logical restore 지원으로 해석하지 않는다.

## 호스트 이력을 잃은 경우

과거 host/launcher/auth root를 그대로 운영 root에 덮어쓰지 않는다. 별도의
private `STATE`와 `PROJECT`에 백업과 정확히 일치하는 pinned 프로그램을
먼저 설치한다. 새 deployment/runner와 새 MCP 인증, 비어 있는 앱 generation이
필요하다. 원래 archive owner는 `SOURCE_STATE`로 읽는다.

```sh
umask 077
loki host application-restore compose prepare-new-deployment \
  --state-root "$STATE" --project "$PROJECT" \
  --source-state-root "$SOURCE_STATE" --backup-id "$BACKUP_ID" \
  > "$STATE/restore-plan.json"
```

이후 apply/recover는 위와 같다. 새 owner로는 암호화 app artifact와 검증된
공개 메타데이터만 복사한다. 과거 physical volume, coordination, launcher,
session, auth와 host journal은 가져오지 않는다. 과거 인증은 새 배포의
인증으로 사용할 수 없다. 원래 백업과 운영 데이터는 보존한다.

## 검증 범위

무료 GitHub Actions의 `ubuntu-24.04`와 `ubuntu-24.04-arm`에서 normal Compose,
retained-history restore, fresh deployment recovery, 두 실제 이미지의 paired
restore를 각각 실행한다. paired fixture는 A값 백업 뒤 프로그램/값을 B로
바꾸고 원래 A프로그램과 새 데이터 generation을 복원해 private delivery의
A값을 메모리에서 비교한다. private 값이나 원문 hash를 evidence에 남기지 않는다.
native migration의 두 실제 OS boot는 같은 operation 복구를 검증하며,
독립 rollback은 별도 VM job에서 실행한다. 이 결과는 임의의 구 바이너리와
새 contract 사이의 호환성을 보장하지 않는다.
