import typing
import unittest
from comfy_unix_entry import schema_alias, flash_device_guard


class SchemaAliasTests(unittest.TestCase):
    def test_equivalent_list_spellings(self):
        self.assertEqual(schema_alias(typing.List[int]), list[int])
        self.assertEqual(schema_alias(typing.Optional[typing.List[int]]), typing.Optional[list[int]])
        self.assertEqual(schema_alias(typing.List[typing.List[float]]), list[list[float]])

    def test_other_annotations_are_not_reinterpreted(self):
        self.assertIs(schema_alias(str), str)
        self.assertEqual(schema_alias(typing.Sequence[int]), typing.Sequence[int])

    def test_cpu_offload_does_not_query_cuda_and_gpu_choices_preserved(self):
        calls=[]
        def available(device):
            calls.append(device)
            return device != 1
        guarded=flash_device_guard(available)
        class CPU:
            type='cpu'
        for device in ('cpu','cpu:0','mps','xpu',CPU()):
            self.assertFalse(guarded(device))
        self.assertEqual(calls,[])
        for device in (None,0,'cuda:0'):
            self.assertTrue(guarded(device))
        self.assertFalse(guarded(1))
        self.assertEqual(calls,[None,0,'cuda:0',1])
        self.assertIs(flash_device_guard(guarded),guarded)


if __name__ == '__main__':unittest.main()
