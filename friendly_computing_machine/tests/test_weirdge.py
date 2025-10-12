from enum import StrEnum

# This file doesn't provide any meaningful coverage
# rather it's a way to write down the little experiments that I normally write in the terminal
# in the code, so that they are hopefully not lost when it comes time to re-understand how they work


class TestStrEnumUnderstanding:
    class EnumForTest(StrEnum):
        """
        Test enum to understand how StrEnum works.
        """

        VALUE1 = "value1"
        VALUE2 = "value_2"

    def test_enum_values(self):
        # yes I can compare directly to strings, very cool
        assert self.EnumForTest.VALUE1 == "value1"
        assert self.EnumForTest.VALUE2 == "value_2"
