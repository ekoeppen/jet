#include "mobj.h"
#include "CppUTest/TestHarness.h"

TEST_GROUP(Object) {
  //TEST_SETUP() {}
  //TEST_TEARDOWN() {}
};

TEST(Object, ValueSize) {
  CHECK_EQUAL(2, sizeof (Val))
}

TEST(Object, DefaultIsNil) {
  Val v;
  CHECK_TRUE(v.isNil())
  CHECK_EQUAL(Val::PTR, v.type())
}

TEST(Object, IntVal) {
  Val v = -123;
  CHECK_FALSE(v.isNil())
  CHECK_EQUAL(Val::INT, v.type())
  CHECK_EQUAL(-123, (int) v)
}

TEST(Object, StrVal) {
  Val v = "abc";
  CHECK_FALSE(v.isNil())
  CHECK_EQUAL(Val::PTR, v.type())
  STRCMP_EQUAL("abc", v)
}

TEST(Object, ChunkSize) {
  if (sizeof (void*) == 4)
    CHECK_EQUAL(8, sizeof (Chunk))
  else
    CHECK_EQUAL(16, sizeof (Chunk))
}