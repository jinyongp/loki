# P04 inventory 봉인과 byte 검증

assemble의 실제 regular-file hash/길이 inventory는 assets 밖에 저장한다. source/release/run/attempt/조립 전 lock digest와 unit archive 관계를 묶는다. 읽기 전용 gate는 기존 exact proof cohort를 판정하고 최종 lock·checksum·inventory를 봉인한다. publisher는 overlay 후 동일 verifier로 정확한 파일 집합과 모든 실제 bytes를 확인한 다음 main SHA/tag/Releaseway 순서로 진행한다.

publish=false는 gate가 실제 assets를 받은 뒤 accepted metadata를 다시 적용하고 같은 verifier를 실행한다. publish=true gate는 작은 metadata만 받는다. Pages와 게시 권한 경계는 유지했다. 기존 exact-source recovery는 immutable 공개 inventory에 맞는 원래 lock/checksum bytes를 보존한다.

리뷰 1: metadata만 받은 gate에서 기존 디렉터리 순회로 checksum을 만들면 archive가 빠지는 결함을 확인했다. 검증된 per-file inventory에서 checksum을 만들고 최종 lock digest만 갱신하도록 수정했다.

리뷰 2: validation-only payload 다운로드가 final lock을 덮는 경계를 확인하고 accepted overlay 후 verify하도록 했다. 추가·누락·digest·길이·directory·symlink·checksum·외부 cohort·미래 attempt·unit binding·공개 recovery 변조를 점검했다. JSON 중복 키도 거부한다. publisher verifier는 tag 앞이다. 남은 범위 내 결함 없음.

실제 native CI와 publish=false 수용은 P05에서 별도로 기록한다. 실제 공개 게시/recovery는 실행하지 않았다.

검증: release Python 회귀 38개, Go workflow 계약 3개와 Python compileall 통과. actionlint는 로컬 PATH에 없어 P05에서 고정 버전으로 준비한다.
