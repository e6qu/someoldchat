. as $versions
| [$root, $root + "-amd64", $root + "-arm64"][]
| . as $tag
| ([$versions[] | select((.metadata.container.tags // []) == [$tag])] | length) as $count
| select($count != 1)
| "\($tag): expected one singleton package version, observed \($count)"
