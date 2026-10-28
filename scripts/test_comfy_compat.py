import typing
import unittest
from comfy_unix_entry import schema_alias


class SchemaAliasTests(unittest.TestCase):
    def test_equivalent_list_spellings(self):
        self.assertEqual(schema_alias(typing.List[int]), list[int])
        self.assertEqual(schema_alias(typing.Optional[typing.List[int]]), typing.Optional[list[int]])
        self.assertEqual(schema_alias(typing.List[typing.List[float]]), list[list[float]])

    def test_other_annotations_are_not_reinterpreted(self):
        self.assertIs(schema_alias(str), str)
        self.assertEqual(schema_alias(typing.Sequence[int]), typing.Sequence[int])


if __name__ == '__main__':unittest.main()
