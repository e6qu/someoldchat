package mutation

// survivingGuardCeiling is how many operations can currently lose every guard
// standing in front of them with every suite still green. Each one is an
// operation whose authorization nothing asserts. The number only shrinks.
//
// Most of it has a single cause, and the matrix now names it: an operation that
// refuses a caller who may not act with "not found" refuses a caller who may
// act the same way when the object is not there, so the matrix was accepting
// evidence equally consistent with no enforcement. See
// refusalDoesNotDistinguishTheHolder in tests/authorization. Giving the fixture
// an object each operation can find is what lowers both numbers together, and
// it does: seeding a canvas, a list, a call, a reminder, a bookmark, a user
// group, a file, a workflow and a real app took this from 102 to 90 and that
// list from 138 to 108 in one change, and a second batch — a shared invitation,
// a workflow trigger and run, and a reaction, pin, star and saved item on the
// seeded message — took them to 87 and 104. A third — installing the app,
// starting a huddle, and giving the workspace an external authorization token —
// took them to 83 and 97, and the token was the one that exposed a real hole:
// DeleteExternalAuthToken checked nothing about its caller, and looked guarded
// only because there was never a token for it to find.
//
// 83 to 82: adding RevokeDeveloperAppTokens would have raised this, because an
// owner-scoped operation's workspace-membership guard is shadowed by its
// ownership check for every caller the matrix drives — the guard's one distinct
// job is refusing a deactivated OWNER, whom the ownership check still recognises.
// A service test that deactivates the owner and asserts the refusal makes that
// job load-bearing, and it does so for the pre-existing IssueDeveloperAppToken
// too, so the pair left the set together rather than one joining it.
//
// Adding list-item comments held the number here rather than raising it. Their
// read guard is shadowed by the store's access check exactly as CanvasComments'
// is, so ListItemComments joins the set; but the same deactivated-author test
// that makes DeleteListItemComment load-bearing was applied to the pre-existing
// DeleteCanvasComment too, which was surviving for the identical reason, so one
// operation left the set as one joined it.
//
// 82 to 78: the authorization audit made the matrix's probe reach each
// operation's own front door as the holder, so a deleted guard is now caught by
// a distinguishing answer where it used to run on to "not found". Seeding a list
// row, a holder-owned reaction, scheduled status, saved item and draft, an
// Activity view and sidebar section, and the seeded conversation's own canvas —
// and handing the probe the one valid string, id, slice, level, role, duration
// or instant each operation needs — moved refusalDoesNotDistinguishTheHolder
// from 103 to 62 and this number with it, a batch at a time, as each batch of
// the audit landed.
//
// 78 to 77: the workflow batch handed the probe the owner's workflow version and
// a valid trigger permission, so DeleteWorkflow's manager guard is now
// load-bearing where it used to run on to an optimistic-version conflict.
//
// 77 to 76: seeding the fixture canvas a revision and the list a finished
// download made RestoreCanvasRevision's write-access guard load-bearing where it
// used to run on to not-found.
//
// 76 to 75: giving the fixture list a column and the workspace a group direct
// message made RemoveListColumn and ConvertGroupDirectToPrivate reach their front
// doors, so one more guard is caught where it used to run on to not-found.
const survivingGuardCeiling = 75
