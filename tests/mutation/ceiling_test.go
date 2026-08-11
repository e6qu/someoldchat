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
const survivingGuardCeiling = 83
