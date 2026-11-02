#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Manage operator-installed InferNex diagnostic Skills.

Usage:
  sudo configure-skills.sh --install DIR
  sudo configure-skills.sh --remove NAME --confirm
  configure-skills.sh --list

A Skill directory must be named after its frontmatter `name` and may contain
only SKILL.md plus regular references/*.md files. Scripts, symlinks, nested
directories, and executable content are not installed. A successful change
restarts an already-running Agent; a failed restart restores the old Skill.
EOF
}

agent_binary="/opt/infernex-agent/bin/infernex-agent"
skill_root="/etc/infernex-agent/skills.d"
backup_root="/var/lib/infernex-agent/backups/skills"
install_source=""
remove_name=""
list_only="false"
confirm="false"

while (($#)); do
  case "$1" in
    --install)
      [[ $# -ge 2 ]] || { echo "--install requires a directory" >&2; exit 2; }
      install_source="$2"
      shift 2
      ;;
    --remove)
      [[ $# -ge 2 ]] || { echo "--remove requires a name" >&2; exit 2; }
      remove_name="$2"
      shift 2
      ;;
    --list)
      list_only="true"
      shift
      ;;
    --confirm)
      confirm="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

selected=0
[[ -n "$install_source" ]] && ((selected += 1))
[[ -n "$remove_name" ]] && ((selected += 1))
[[ "$list_only" == "true" ]] && ((selected += 1))
((selected == 1)) || { usage >&2; exit 2; }
[[ -x "$agent_binary" ]] || { echo "InferNex Agent binary is missing: ${agent_binary}" >&2; exit 1; }

if [[ "$list_only" == "true" ]]; then
  exec "$agent_binary" skills list \
    --directories "/opt/infernex-agent/skills,${skill_root}"
fi

((EUID == 0)) || { echo "install/remove requires root" >&2; exit 1; }
install -d -m 0755 -o root -g root "$skill_root"
install -d -m 0700 -o root -g root "$backup_root"

valid_name() {
  [[ "$1" =~ ^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$ ]]
}

restart_if_active() {
  if systemctl is-active --quiet infernex-agent.service; then
    systemctl restart infernex-agent.service
    systemctl is-active --quiet infernex-agent.service
  fi
}

if [[ -n "$install_source" ]]; then
  source_path="$(readlink -f -- "$install_source")"
  [[ -d "$source_path" && ! -L "$source_path" ]] || { echo "Skill source must be a real directory" >&2; exit 1; }
  name="$(basename -- "$source_path")"
  valid_name "$name" || { echo "invalid Skill directory name: ${name}" >&2; exit 1; }
  [[ ! -e "/opt/infernex-agent/skills/${name}" ]] || { echo "cannot override built-in Skill: ${name}" >&2; exit 1; }
  [[ -z "$(find "$source_path" -type l -print -quit)" ]] || { echo "Skill symlinks are not allowed" >&2; exit 1; }
  while IFS= read -r entry; do
    relative="${entry#"$source_path"/}"
    [[ "$entry" != "$source_path" ]] || continue
    if [[ -d "$entry" ]]; then
      [[ "$relative" == "references" ]] || { echo "unsupported Skill directory: ${relative}" >&2; exit 1; }
    elif [[ -f "$entry" ]]; then
      case "$relative" in
        SKILL.md | references/*.md) ;;
        *) echo "unsupported Skill file: ${relative}" >&2; exit 1 ;;
      esac
    else
      echo "unsupported special file: ${relative}" >&2
      exit 1
    fi
  done < <(find "$source_path" -mindepth 1 -print)
  "$agent_binary" skills validate --path "$source_path" >/dev/null

  stage_root="$(mktemp -d "${skill_root}/.install.XXXXXX")"
  previous="${skill_root}/.previous-${name}-$$"
  target="${skill_root}/${name}"
  cleanup() {
    [[ "$stage_root" == "${skill_root}/.install."* ]] && rm -rf -- "$stage_root"
    [[ "$previous" == "${skill_root}/.previous-${name}-"* && -e "$previous" ]] && rm -rf -- "$previous"
  }
  trap cleanup EXIT
  install -d -m 0755 -o root -g root "${stage_root}/${name}"
  install -m 0644 -o root -g root "$source_path/SKILL.md" "${stage_root}/${name}/SKILL.md"
  if [[ -d "$source_path/references" ]]; then
    install -d -m 0755 -o root -g root "${stage_root}/${name}/references"
    while IFS= read -r reference; do
      install -m 0644 -o root -g root "$reference" "${stage_root}/${name}/references/$(basename -- "$reference")"
    done < <(find "$source_path/references" -maxdepth 1 -type f -name '*.md' -print | LC_ALL=C sort)
  fi
  "$agent_binary" skills validate --path "${stage_root}/${name}" >/dev/null

  was_present="false"
  if [[ -e "$target" ]]; then
    [[ -d "$target" && ! -L "$target" ]] || { echo "unsafe existing Skill target: ${target}" >&2; exit 1; }
    was_present="true"
    timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
    backup="${backup_root}/${timestamp}-${name}"
    cp -a -- "$target" "$backup"
    mv -- "$target" "$previous"
  fi
  mv -- "${stage_root}/${name}" "$target"
  if ! restart_if_active; then
    rm -rf -- "$target"
    if [[ "$was_present" == "true" ]]; then mv -- "$previous" "$target"; fi
    restart_if_active || true
    echo "Agent restart failed; previous Skill restored" >&2
    exit 1
  fi
  [[ "$was_present" == "false" ]] || rm -rf -- "$previous"
  echo "Installed Skill: ${name}"
  "$agent_binary" skills list --directories "/opt/infernex-agent/skills,${skill_root}"
  exit 0
fi

valid_name "$remove_name" || { echo "invalid Skill name: ${remove_name}" >&2; exit 1; }
[[ "$confirm" == "true" ]] || { echo "--confirm is required to remove a Skill" >&2; exit 1; }
target="${skill_root}/${remove_name}"
[[ -d "$target" && ! -L "$target" ]] || { echo "user Skill not found: ${remove_name}" >&2; exit 1; }
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="${backup_root}/${timestamp}-${remove_name}"
mv -- "$target" "$backup"
if ! restart_if_active; then
  mv -- "$backup" "$target"
  restart_if_active || true
  echo "Agent restart failed; removed Skill restored" >&2
  exit 1
fi
echo "Removed Skill: ${remove_name}; recoverable backup: ${backup}"
