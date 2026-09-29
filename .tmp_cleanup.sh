#!/bin/bash
set -e
cd /mnt/e/缓存/ClearDNS/overture
GIT="git -c http.proxy=http://127.0.0.1:10808 -c https.proxy=http://127.0.0.1:10808"
TOKEN=$(cat /mnt/e/PortableGit/令牌.txt)
rm -f .tmp_push213.sh
# list any stray temp files
ls -a | grep -E '^\.tmp_' || echo NO_TEMP_FILES
git add -A
git status --short
git commit -m "chore: remove stray temp script committed with v2.1.3"
echo '---PUSH---'
$GIT push "https://x-access-token:${TOKEN}@github.com/xiaoran0503/overture.git" master 2>&1 | tail -2
echo '---VERIFY---'
$GIT log --oneline -3
