#!/bin/bash
set -e
cd /mnt/e/缓存/ClearDNS/overture
GIT="git -c http.proxy=http://127.0.0.1:10808 -c https.proxy=http://127.0.0.1:10808"
TOKEN=$(cat /mnt/e/PortableGit/令牌.txt)
$GIT config user.name "xiaoran0503"
$GIT config user.email "74979273+xiaoran0503@users.noreply.github.com"
rm -f .tmp_cfgver.py .tmp_cfgver2.sh .tmp_v213.py .tmp_smoke213.sh .tmp_verify3.sh .tmp_verify4.sh
git add -A
git status --short
$GIT commit --file=.tmp_commit_msg.txt
$GIT tag v2.1.3
echo '---PUSH MASTER---'
$GIT push "https://x-access-token:${TOKEN}@github.com/xiaoran0503/overture.git" master 2>&1 | tail -2
echo '---PUSH TAG---'
$GIT push "https://x-access-token:${TOKEN}@github.com/xiaoran0503/overture.git" v2.1.3 2>&1 | tail -2
echo '---VERIFY---'
$GIT ls-remote "https://x-access-token:${TOKEN}@github.com/xiaoran0503/overture.git" | grep -E 'refs/heads/master|refs/tags/v2.1.3'
$GIT log --oneline -1
rm -f .tmp_commit_msg.txt
