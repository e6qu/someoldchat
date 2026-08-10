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
// an object each operation can find is what lowers both numbers together.
const survivingGuardCeiling = 102
