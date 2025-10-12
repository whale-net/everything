from sqlmodel import MetaData, SQLModel


class Base(SQLModel):
    # set schema. not required because isolating apps+environments via database
    # but may as well specify to avoid ambiguity
    metadata = MetaData(schema="fcm")

    # def to_dict(self) -> Dict[str, Any]:
    #     """Convert model to a dictionary with JSON serializable values."""
    #     data = self.model_dump()
    #     # Convert all datetime objects to ISO format strings
    #     for key, value in data.items():
    #         if isinstance(value, datetime.datetime):
    #             data[key] = value.isoformat()
    #     return data

    # @classmethod
    # def from_dict(cls, data: Dict[str, Any]):
    #     """Create a model instance from a dictionary."""
    #     # Make a copy to avoid modifying the original
    #     data_copy = data.copy()
    #     # Parse ISO format strings back to datetime objects
    #     for key, value in data_copy.items():
    #         if isinstance(value, str) and key in cls.__annotations__:
    #             if cls.__annotations__[key] == datetime.datetime:
    #                 try:
    #                     data_copy[key] = datetime.datetime.fromisoformat(value)
    #                 except ValueError:
    #                     pass  # Not a valid datetime string
    #     return cls(**data_copy)


"""
# Trying out a different way to build these models, copied from a tutorial that seems to have a better grasp on this
# than I did when working on manman
# So, rather than having one model for everything, going to create multiple classes that work together
# unsure of benefits or drawbacks as of writing

# one benefit appears to be easier separation between what is required for api/function consumers
# any create action doesn't have (and usually cannot have) an ID, but it should always return one
# this is possible to communicate through different models with a shared hierarchy

# IMPLEMENTATION MODEL:
class MyClassBase:
    ...

class MyClass(MyClassBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)

class MyClassCreate(MyClassBase):
    pass
"""
