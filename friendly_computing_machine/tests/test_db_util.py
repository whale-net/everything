"""Unit tests for db/util.py functions."""

import pytest
from sqlalchemy.exc import SQLAlchemyError
from sqlmodel import Field, SQLModel

# Assuming db_util is in src/friendly_computing_machine/db/util.py
# Import only what's actually in util.py and used by these tests
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    db_update,
)


class SomeModel(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    name: str
    value: float | None = None


# Test db_update
class TestDbUpdate:
    def test_attributes_updated_correctly(self, mock_session):  # Uses conftest fixture
        mock_instance = SomeModel(id=1, name="Original Name", value=10.0)
        mock_session.get.return_value = mock_instance

        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={"name": "Updated Name", "value": 20.0},
        )

        assert response_instance == mock_instance
        assert mock_instance.name == "Updated Name"
        assert mock_instance.value == 20.0
        mock_session.get.assert_called_once_with(SomeModel, 1)
        mock_session.add.assert_not_called()  # db_update doesn't call add for existing instances
        mock_session.commit.assert_called_once()
        mock_session.refresh.assert_called_once_with(mock_instance)

    def test_instance_not_found(self, mock_session):  # Uses conftest fixture
        mock_session.get.return_value = None
        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=99,
            updates={"name": "New Name"},
        )

        assert response_instance is None  # db_update returns None if instance not found
        mock_session.get.assert_called_once_with(SomeModel, 99)
        mock_session.add.assert_not_called()
        mock_session.commit.assert_not_called()
        mock_session.refresh.assert_not_called()

    def test_invalid_field_no_update_and_returns_none(
        self, mock_session
    ):  # Uses conftest fixture
        mock_instance = SomeModel(id=1, name="Original Name")
        mock_session.get.return_value = mock_instance

        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={"non_existent_field": "some_value"},
        )
        # db_update with only invalid fields (as per util.py's validate_model_fields)
        # will result in valid_updates being empty, so it returns None.
        assert response_instance is None
        assert mock_instance.name == "Original Name"  # Should not change
        # mock_session.get is not called if valid_updates is empty
        mock_session.get.assert_not_called()
        mock_session.add.assert_not_called()
        mock_session.commit.assert_not_called()
        mock_session.refresh.assert_not_called()

    def test_update_allows_type_changes_via_setattr(
        self, mock_session
    ):  # Uses conftest fixture
        mock_instance = SomeModel(id=1, name="Original Name", value=10.0)
        mock_session.get.return_value = mock_instance

        # SQLModel/Pydantic might not validate on setattr, so let's test what actually happens
        result = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={"name": None},  # Attempt to set name to None
        )

        # If no error is raised, the update should succeed
        assert result == mock_instance
        assert mock_instance.name is None  # SQLModel allows this assignment
        mock_session.get.assert_called_once_with(SomeModel, 1)
        mock_session.add.assert_not_called()
        mock_session.commit.assert_called_once()  # Commit is called if no error
        mock_session.refresh.assert_called_once_with(mock_instance)

    def test_update_pk_if_present_in_updates(
        self, mock_session
    ):  # Uses conftest fixture
        # util.py's validate_model_fields doesn't distinguish PKs, it just checks field existence.
        # So, if 'id' is in updates, db_update will attempt to set it.
        mock_instance = SomeModel(id=1, name="Original Name")
        mock_session.get.return_value = mock_instance

        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={"id": 2, "name": "New Name PK"},
        )
        assert response_instance == mock_instance
        assert mock_instance.id == 2  # PK is updated
        assert mock_instance.name == "New Name PK"
        mock_session.get.assert_called_once_with(SomeModel, 1)
        mock_session.add.assert_not_called()
        mock_session.commit.assert_called_once()
        mock_session.refresh.assert_called_once_with(mock_instance)

    def test_sqlalchemy_error_on_commit_propagates(
        self, mock_session
    ):  # Uses conftest fixture
        # The current db_update in util.py does not have try-except around session.commit()
        # to perform a session.rollback(). So, the error will propagate.
        mock_instance = SomeModel(id=1, name="Original Name")
        mock_session.get.return_value = mock_instance
        mock_session.commit.side_effect = SQLAlchemyError("Commit failed")

        with pytest.raises(SQLAlchemyError, match="Commit failed"):
            db_update(
                session=mock_session,
                model_class=SomeModel,
                model_id=1,
                updates={"name": "New Name"},
            )

        mock_session.get.assert_called_once_with(SomeModel, 1)
        mock_session.add.assert_not_called()
        mock_session.commit.assert_called_once()  # Commit is attempted
        mock_session.rollback.assert_not_called()  # Rollback is NOT called by current util.py
        mock_session.refresh.assert_not_called()  # Refresh is not called if commit fails

    def test_no_data_to_update_returns_none(
        self, mock_session
    ):  # Uses conftest fixture
        mock_instance = SomeModel(id=1, name="Original Name")
        mock_session.get.return_value = mock_instance

        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={},  # No data
        )
        # db_update returns None if valid_updates (from validate_model_fields) is empty
        assert response_instance is None
        # mock_session.get is not called if updates is empty
        mock_session.get.assert_not_called()
        mock_session.add.assert_not_called()
        mock_session.commit.assert_not_called()
        mock_session.refresh.assert_not_called()

    def test_update_with_none_value(self, mock_session):  # Uses conftest fixture
        mock_instance = SomeModel(id=1, name="Original Name", value=10.0)
        mock_session.get.return_value = mock_instance

        response_instance = db_update(
            session=mock_session,
            model_class=SomeModel,
            model_id=1,
            updates={"value": None},  # Setting an existing field to None
        )

        assert response_instance == mock_instance
        assert mock_instance.name == "Original Name"  # Unchanged
        assert mock_instance.value is None  # Updated to None
        mock_session.get.assert_called_once_with(SomeModel, 1)
        mock_session.add.assert_not_called()
        mock_session.commit.assert_called_once()
        mock_session.refresh.assert_called_once_with(mock_instance)
