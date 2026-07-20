# A release consists of three distinct package versions with exactly one tag
# each: a 12-character commit root and its -amd64/-arm64 siblings.
def release_part:
  (.metadata.container.tags // []) as $tags
  | if ($tags | length) != 1 then null
    elif $tags[0] | test("^[0-9a-f]{12}$") then
      {root: $tags[0], kind: "root"}
    elif $tags[0] | test("^[0-9a-f]{12}-amd64$") then
      {root: ($tags[0] | sub("-amd64$"; "")), kind: "amd64"}
    elif $tags[0] | test("^[0-9a-f]{12}-arm64$") then
      {root: ($tags[0] | sub("-arm64$"; "")), kind: "arm64"}
    else null
    end;

. as $versions
| ([$versions[]
    | . as $version
    | (release_part) as $part
    | select($part != null)
    | $part + {id: $version.id, created_at: $version.created_at}]) as $parts
| ($parts
   | group_by(.root)
   | map(
       select(length == 3 and (map(.kind) | sort) == ["amd64", "arm64", "root"])
       | {
           root: .[0].root,
           created_at: (map(.created_at) | max),
           ids: map(.id)
         }
     )
   | sort_by(.created_at, .root)
   | reverse
   | .[:$keep]
   | map(.ids[])) as $kept_ids
| $versions[]
| select(.id as $id | $kept_ids | index($id) == null)
| .id
