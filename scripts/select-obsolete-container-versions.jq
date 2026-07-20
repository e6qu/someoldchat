# A release consists of a 12-character commit tag and its -amd64/-arm64 tags.
# Keep the newest requested roots and delete every other tagged version.
def release_tags:
  [.metadata.container.tags[]? | select(test("^[0-9a-f]{12}$"))];

(map(select((release_tags | length) > 0))
 | sort_by(.created_at)
 | reverse
 | .[:$keep]) as $releases
| ($releases
   | map(release_tags[])
   | map(., . + "-amd64", . + "-arm64")
   | unique) as $kept_tags
| map(
    select((.metadata.container.tags // [] | length) > 0)
    | select(all(.metadata.container.tags[]?; . as $tag | $kept_tags | index($tag) == null))
  )
| .[].id
